package view

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/httpclient"
)

type Service struct {
	HTTPClient *http.Client
	// ResolveHeaders expands deployment credentials server-side. Never expose
	// these headers (or upstream errors/bodies) in a rendering document.
	ResolveHeaders func(connectorID string, headers map[string]string) (map[string]string, error)
}

func (s *Service) Resolve(ctx context.Context, mounts []Mount, ref Reference, usage string) (Document, error) {
	if err := ref.Validate(); err != nil {
		return Document{}, err
	}
	if usage != "display" && usage != "form" {
		return Document{}, fmt.Errorf("%w: usage", ErrInvalid)
	}
	for _, mount := range mounts {
		if mount.ID != ref.ConnectorID {
			continue
		}
		d, ok := mount.Views[ref.Key]
		if !ok {
			return Document{}, ErrNotFound
		}
		if !d.Supports(usage) {
			return Document{}, fmt.Errorf("%w: unsupported usage", ErrInvalid)
		}
		doc := Document{View: Reference{ConnectorID: mount.ID, Key: ref.Key, Version: mount.Version, Renderer: d.Renderer}, Entry: d.Entry}
		if d.Remote != nil {
			data, err := s.fetch(ctx, mount.ID, ref.Key, d.Renderer, *d.Remote)
			if err != nil {
				return Document{}, err
			}
			if err := setEntry(&doc, d.Renderer, data); err != nil {
				return Document{}, err
			}
		} else {
			data, err := readResource(mount.Dir, d.Entry)
			if err != nil {
				return Document{}, err
			}
			if err := setEntry(&doc, d.Renderer, data); err != nil {
				return Document{}, err
			}
			for _, asset := range d.Assets {
				data, err := readResource(mount.Dir, asset)
				if err != nil {
					return Document{}, err
				}
				mediaType := mime.TypeByExtension(filepath.Ext(asset))
				if mediaType == "" {
					mediaType = "application/octet-stream"
				}
				doc.Assets = append(doc.Assets, Asset{Path: asset, MediaType: mediaType, Data: data})
			}
		}
		if _, err := documentBytes(doc); err != nil {
			return Document{}, err
		}
		return doc, nil
	}
	return Document{}, ErrNotFound
}

func readResource(root, name string) ([]byte, error) {
	if !ValidResourcePath(name) {
		return nil, fmt.Errorf("%w: resource path", ErrInvalid)
	}
	info, err := os.Lstat(filepath.Join(root, "views"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: views must be a real directory", ErrInvalid)
	}
	// Root prevents both lexical traversal and symlink escapes during the open.
	r, err := os.OpenRoot(filepath.Join(root, "views"))
	if err != nil {
		return nil, fmt.Errorf("%w: package directory", ErrInvalid)
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(strings.TrimPrefix(name, "views/")))
	if err != nil {
		return nil, fmt.Errorf("%w: view resource %s", ErrInvalid, name)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: resource must be a regular file", ErrInvalid)
	}
	return readBounded(f)
}

func readBounded(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: document exceeds 8 MiB", ErrInvalid)
	}
	return data, nil
}

func decodeEntry(renderer string, data []byte) (map[string]any, error) {
	if !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: entry must be nonempty UTF-8", ErrInvalid)
	}
	if renderer == "html" {
		return nil, nil
	}
	var qlc map[string]any
	if err := json.Unmarshal(data, &qlc); err != nil || qlc == nil {
		return nil, fmt.Errorf("%w: QLC must be an object", ErrInvalid)
	}
	return qlc, nil
}

func setEntry(doc *Document, renderer string, data []byte) error {
	qlc, err := decodeEntry(renderer, data)
	if err != nil {
		return err
	}
	if renderer == "html" {
		doc.HTML = string(data)
	} else {
		doc.QLC = qlc
	}
	return nil
}

func (s *Service) fetch(ctx context.Context, connectorID, key, renderer string, remote Remote) ([]byte, error) {
	if err := validateRemote(remote); err != nil {
		return nil, err
	}
	timeout := remote.TimeoutMS
	if timeout == 0 {
		timeout = 10000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	if remote.Key != "" {
		key = remote.Key
	}
	method, keyField := "views/get", "key"
	if remote.Protocol == "legacy-viewport" {
		method, keyField = "viewports/get", "viewportKey"
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "view", "method": method, "params": map[string]any{keyField: key}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, remote.URL, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	headers := remote.Headers
	if s != nil && s.ResolveHeaders != nil {
		headers, err = s.ResolveHeaders(connectorID, headers)
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	for k, v := range headers {
		if strings.Contains(v, "${") {
			return nil, ErrUnavailable
		}
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	client := *httpclient.NewClient(0)
	if s != nil && s.HTTPClient != nil {
		client = *s.HTTPClient
	}
	// A template endpoint must not redirect credentials or requests elsewhere.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrUnavailable
	}
	data, err := readBounded(resp.Body)
	if err != nil {
		return nil, ErrUnavailable
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &rpc) != nil || len(rpc.Error) > 0 && string(rpc.Error) != "null" {
		return nil, ErrUnavailable
	}
	var result struct {
		Renderer     string          `json:"renderer"`
		ViewportType string          `json:"viewportType"`
		Payload      json.RawMessage `json:"payload"`
		HTML         *string         `json:"html"`
		QLC          json.RawMessage `json:"qlc"`
	}
	if json.Unmarshal(rpc.Result, &result) != nil {
		return nil, ErrUnavailable
	}
	actual := result.Renderer
	if actual == "" {
		actual = result.ViewportType
	}
	if actual != "" && actual != renderer {
		return nil, ErrUnavailable
	}
	if renderer == "html" {
		if result.HTML != nil {
			return []byte(*result.HTML), nil
		}
		var html string
		if json.Unmarshal(result.Payload, &html) != nil {
			return nil, ErrUnavailable
		}
		return []byte(html), nil
	}
	if len(result.QLC) > 0 {
		return result.QLC, nil
	}
	if len(result.Payload) > 0 {
		return result.Payload, nil
	}
	return nil, ErrUnavailable
}
