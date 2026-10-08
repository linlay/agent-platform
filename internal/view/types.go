// Package view owns presentation definitions and immutable rendering documents.
// It does not execute tools, resolve HITL decisions, or depend on Agent/catalog.
package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	ErrNotFound    = errors.New("view not found")
	ErrUnavailable = errors.New("view unavailable")
	ErrInvalid     = errors.New("invalid view")
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const MaxDocumentBytes = 8 << 20

// Reference is a configured identity plus server-resolved snapshot metadata.
// Configuration accepts only connectorId and key; clients must not select paths.
type Reference struct {
	Source      string `json:"source,omitempty"`
	ConnectorID string `json:"connectorId,omitempty"`
	Key         string `json:"key"`
	Version     string `json:"version,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Renderer    string `json:"renderer,omitempty"`
}

func (r Reference) Validate() error {
	if (r.Source != "" && r.Source != "builtin" && r.Source != "connector") || (r.ConnectorID == "" && r.Source == "connector") || (r.ConnectorID != "" && r.Source == "builtin") || (r.ConnectorID != "" && !idPattern.MatchString(r.ConnectorID)) || !idPattern.MatchString(r.Key) || (r.Hash != "" && !hashPattern.MatchString(r.Hash)) {
		return fmt.Errorf("%w: connectorId, key or hash", ErrInvalid)
	}
	if r.ConnectorID == "" {
		if r.Hash != "" || r.Version != "" {
			return ErrInvalid
		}
		ref, err := ResolveBuiltin(r.Key)
		if err != nil {
			return err
		}
		if r.Renderer != "" && r.Renderer != ref.Renderer {
			return ErrInvalid
		}
	}
	return nil
}

func ParseReference(value any) (*Reference, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: reference", ErrInvalid)
	}
	if string(data) == "null" {
		return nil, nil
	}
	var ref Reference
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&ref); err != nil {
		return nil, fmt.Errorf("%w: reference", ErrInvalid)
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	return &ref, nil
}

func ParseConfigReference(value any) (*Reference, error) {
	ref, err := ParseReference(value)
	if err != nil || ref == nil {
		return ref, err
	}
	if ref.Source != "" || ref.Version != "" || ref.Hash != "" || ref.Renderer != "" {
		return nil, fmt.Errorf("%w: configuration accepts only connectorId and key", ErrInvalid)
	}
	return ref, nil
}

func (r Reference) Map() map[string]any {
	result := map[string]any{"key": r.Key, "source": r.Source}
	if r.ConnectorID != "" {
		result["connectorId"] = r.ConnectorID
		if r.Source == "" {
			result["source"] = "connector"
		}
	} else {
		result["source"] = "builtin"
	}
	if r.Version != "" {
		result["version"] = r.Version
	}
	if r.Hash != "" {
		result["hash"] = r.Hash
	}
	if r.Renderer != "" {
		result["renderer"] = r.Renderer
	}
	return result
}

func Clone(ref *Reference) *Reference {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

type Config struct {
	Views map[string]Definition `json:"views"`
}

type Definition struct {
	Title    string   `json:"title,omitempty"`
	Renderer string   `json:"renderer"`
	Usage    []string `json:"usage"`
	Entry    string   `json:"entry,omitempty"`
	Assets   []string `json:"assets,omitempty"`
	Remote   *Remote  `json:"remote,omitempty"`
}

// Remote fetches only a template by key; no conversation/tool/form data is sent.
type Remote struct {
	URL       string            `json:"url"`
	Key       string            `json:"key,omitempty"`
	Protocol  string            `json:"protocol,omitempty"` // views
	TimeoutMS int               `json:"timeout,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

func (d Definition) Supports(usage string) bool {
	for _, item := range d.Usage {
		if item == usage {
			return true
		}
	}
	return false
}

func ValidateDefinitions(root string, definitions map[string]Definition) error {
	if len(definitions) == 0 {
		return fmt.Errorf("%w: views must not be empty", ErrInvalid)
	}
	for key, d := range definitions {
		if !idPattern.MatchString(key) || (d.Renderer != "html" && d.Renderer != "qlc") {
			return fmt.Errorf("%w: view key or renderer", ErrInvalid)
		}
		seen := map[string]bool{}
		if len(d.Usage) == 0 {
			return fmt.Errorf("%w: usage is required for %s", ErrInvalid, key)
		}
		for _, usage := range d.Usage {
			if (usage != "display" && usage != "form") || seen[usage] {
				return fmt.Errorf("%w: usage for %s", ErrInvalid, key)
			}
			seen[usage] = true
		}
		if d.Remote != nil {
			if d.Entry != "" || len(d.Assets) != 0 {
				return fmt.Errorf("%w: local and remote sources cannot be mixed", ErrInvalid)
			}
			if err := validateRemote(*d.Remote); err != nil {
				return err
			}
		} else {
			if !ValidResourcePath(d.Entry) {
				return fmt.Errorf("%w: entry for %s", ErrInvalid, key)
			}
			paths := append([]string{d.Entry}, d.Assets...)
			seen := map[string]bool{}
			total := 0
			for _, name := range paths {
				if !ValidResourcePath(name) || seen[name] {
					return fmt.Errorf("%w: asset path", ErrInvalid)
				}
				seen[name] = true
				data, err := readResource(root, name)
				if err != nil {
					return err
				}
				total += len(data)
				if total > MaxDocumentBytes {
					return fmt.Errorf("%w: view exceeds 8 MiB", ErrInvalid)
				}
				if name == d.Entry {
					if _, err := decodeEntry(d.Renderer, data); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// View files must live in the dedicated public subtree, never bin/skills/config.
func ValidResourcePath(name string) bool {
	return strings.HasPrefix(name, "views/") && !strings.ContainsAny(name, "\\\x00?#:%") && path.Clean(name) == name && !strings.HasSuffix(name, "/") && len(name) > len("views/")
}

func validateRemote(r Remote) error {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: remote url", ErrInvalid)
	}
	if r.Protocol != "" && r.Protocol != "views" {
		return fmt.Errorf("%w: remote protocol", ErrInvalid)
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > 60000 {
		return fmt.Errorf("%w: remote timeout must be 0..60000 ms", ErrInvalid)
	}
	for key, value := range r.Headers {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, "\r\n\x00") || http.CanonicalHeaderKey(key) == "Host" {
			return fmt.Errorf("%w: remote headers", ErrInvalid)
		}
	}
	return nil
}

type Mount struct {
	ID      string
	Version string
	Dir     string
	Views   map[string]Definition
}

type Asset struct {
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"` // JSON base64; only explicitly declared resources.
}

type Document struct {
	View   Reference      `json:"view"`
	Entry  string         `json:"entry,omitempty"`
	HTML   string         `json:"html,omitempty"`
	QLC    map[string]any `json:"qlc,omitempty"`
	Assets []Asset        `json:"assets,omitempty"`
}

func (r Reference) ConfigMap() map[string]any {
	m := map[string]any{"key": r.Key}
	if r.ConnectorID != "" {
		m["connectorId"] = r.ConnectorID
	}
	return m
}
