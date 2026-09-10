package documentpreview

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Service struct {
	config Config
	dir    string
	slots  chan struct{}
	mu     sync.Mutex
	locks  map[string]chan struct{}
}

func New(c Config, stateDir string) (*Service, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	s := &Service{config: c.normalized(), dir: filepath.Join(stateDir, "document-preview"), slots: make(chan struct{}, 2), locks: map[string]chan struct{}{}}
	if c.Enabled {
		if stateDir == "" {
			return nil, fmt.Errorf("document-preview state directory is required")
		}
		if err := os.MkdirAll(s.dir, 0700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) lock(ctx context.Context, key string) (func(), error) {
	for {
		s.mu.Lock()
		existing := s.locks[key]
		if existing == nil {
			ch := make(chan struct{})
			s.locks[key] = ch
			s.mu.Unlock()
			return func() { s.mu.Lock(); delete(s.locks, key); close(ch); s.mu.Unlock() }, nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-existing:
		}
	}
}

func readSnapshot(resolved Resolved, limit int64) (snapshot, error) {
	var s snapshot
	file, err := os.Open(resolved.Path)
	if err != nil {
		return s, failure("preview_source_missing", "文件不可读取", 404)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return s, failure("preview_invalid_source", "仅支持普通文件", 400)
	}
	if before.Size() > limit {
		return s, failure("preview_file_too_large", "文件超过在线预览大小限制", 413)
	}
	ext := strings.ToLower(filepath.Ext(resolved.Path))
	mainPart := map[string]string{".docx": "word/document.xml", ".pptx": "ppt/presentation.xml", ".xlsx": "xl/workbook.xml"}[ext]
	if mainPart == "" {
		return s, failure("preview_unsupported_type", "在线预览仅支持 DOCX、PPTX、XLSX", 415)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return s, failure("preview_source_unavailable", "读取文件失败", 500)
	}
	if int64(len(data)) > limit {
		return s, failure("preview_file_too_large", "文件超过在线预览大小限制", 413)
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return s, failure("preview_source_changed", "文件正在修改，请刷新后重试", 409)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return s, failure("preview_invalid_document", "文件不是有效的 Office 文档", 415)
	}
	var contentTypes, main bool
	for _, entry := range archive.File {
		if entry.Name == "[Content_Types].xml" {
			contentTypes = true
		}
		if entry.Name == mainPart {
			main = true
		}
	}
	if !contentTypes || !main {
		return s, failure("preview_invalid_document", "文件内容与 Office 格式不匹配", 415)
	}
	sum := sha256.Sum256(data)
	return snapshot{data: data, name: before.Name(), hash: hex.EncodeToString(sum[:])}, nil
}

func (s *Service) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.dir, ".preview-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, name))
}

func (s *Service) read(name string, value any) error {
	file, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(value)
}

func stateError() error {
	return failure("preview_state_unavailable", "无法保存预览状态，请重试", 500)
}

func (s *Service) Prepare(ctx context.Context, subject string, request Request, resolve Resolver) (Result, error) {
	var result Result
	startedAt := time.Now().UnixNano()
	if !s.config.Enabled {
		return result, failure("preview_disabled", "未配置在线预览服务", 503)
	}
	if request.RequestID == "" || len(request.RequestID) > 128 || strings.ContainsAny(request.RequestID, "\r\n\x00") {
		return result, failure("invalid_request", "需要有效 requestId", 400)
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return result, failure("preview_timeout", "预览准备超时", 504)
	}
	// Always resolve and read before looking at a cached result or request binding.
	resolved, err := resolve()
	if err != nil {
		return result, err
	}
	snap, err := readSnapshot(resolved, s.config.MaxFileBytes)
	if err != nil {
		return result, err
	}
	h, err := newHub(s.config)
	if err != nil {
		return result, err
	}
	key := digest(subject, h.scope, resolved.Identity, snap.hash)
	binding := digest(subject, h.scope, request.RequestID)
	releaseBinding, err := s.lock(ctx, "request:"+binding)
	if err != nil {
		return result, failure("preview_timeout", "预览准备超时", 504)
	}
	defer releaseBinding()
	var bound struct {
		Key       string
		ExpiresAt int64
	}
	err = s.read("request-"+binding+".json", &bound)
	if err == nil && bound.Key != key {
		return result, failure("request_id_conflict", "requestId 已绑定其他文件版本，请重新发起", 409)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, stateError()
	}
	bound.Key = key
	bound.ExpiresAt = time.Now().Add(24 * time.Hour).UnixMilli()
	if err = s.write("request-"+binding+".json", bound); err != nil {
		return result, stateError()
	}
	release, err := s.lock(ctx, key)
	if err != nil {
		return result, failure("preview_timeout", "预览准备超时", 504)
	}
	defer release()
	r := record{Key: key, Scope: h.scope, APIBaseURL: h.config.APIBaseURL, PublicBaseURL: h.config.PublicBaseURL, SourceHash: snap.hash}
	err = s.read(key+".json", &r)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, stateError()
	}
	if r.Key != key || r.Scope != h.scope {
		return result, stateError()
	}
	if r.UnknownUpload && (r.UnknownRequest == binding || startedAt <= r.UnknownAt) {
		return result, failure("preview_upload_outcome_unknown", "上次上传结果未知，请重新发起预览；可能存在远端副本", 502)
	}
	r.LastAccess = time.Now().UnixMilli()
	if r.DocumentID != "" {
		if !uuidPattern.MatchString(r.DocumentID) {
			return result, stateError()
		}
		err = h.request(ctx, "GET", "/api/v1/documents/"+r.DocumentID+"/versions", "", nil, nil)
		var remote *Error
		if errors.As(err, &remote) && remote.Code == "preview_remote_missing" {
			r.DocumentID = ""
			r.URL = ""
			r.ExpiresAt = 0
		} else if err != nil {
			return result, err
		}
	}
	if r.DocumentID == "" {
		r.UnknownUpload = true
		r.UnknownRequest = binding
		r.UnknownAt = time.Now().UnixNano()
		if err = s.write(key+".json", r); err != nil {
			return result, stateError()
		}
		id, uploadErr := h.upload(ctx, snap)
		if uploadErr != nil {
			// Requests already waiting on this version share its unknown outcome;
			// only a new explicit request may start another upload attempt.
			r.UnknownAt = time.Now().UnixNano()
			if err = s.write(key+".json", r); err != nil {
				return result, stateError()
			}
			return result, failure("preview_upload_outcome_unknown", "上传失败或结果未知，请重新发起预览；可能存在远端副本", 502)
		}
		r.DocumentID = id
		r.UnknownUpload = false
		r.UnknownRequest = ""
		r.UnknownAt = 0
		// Commit the returned ID before any subsequent remote operation.
		if err = s.write(key+".json", r); err != nil {
			return result, stateError()
		}
	}
	if r.URL == "" || r.ExpiresAt <= time.Now().Add(time.Minute).UnixMilli() {
		r.LinkID, r.URL, r.ExpiresAt, err = h.share(ctx, r.DocumentID)
		if err != nil {
			return result, err
		}
	}
	if err = s.write(key+".json", r); err != nil {
		return result, stateError()
	}
	return Result{PreviewID: key, SourceRevision: "sha256:" + snap.hash, OpenMode: s.config.OpenMode, URL: r.URL, ExpiresAt: r.ExpiresAt}, nil
}

func (s *Service) RunCleanup(ctx context.Context) {
	if !s.config.Enabled {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Cleanup(ctx); err != nil {
				log.Print("document-preview cleanup incomplete; will retry")
			}
		}
	}
}

func (s *Service) Cleanup(ctx context.Context) error {
	if !s.config.Enabled {
		return nil
	}
	h, err := newHub(s.config)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasPrefix(name, "request-") {
			var b struct{ ExpiresAt int64 }
			if s.read(name, &b) == nil && b.ExpiresAt < time.Now().UnixMilli() {
				release, err := s.lock(ctx, "request:"+strings.TrimSuffix(strings.TrimPrefix(name, "request-"), ".json"))
				if err != nil {
					return err
				}
				if s.read(name, &b) == nil && b.ExpiresAt < time.Now().UnixMilli() {
					_ = os.Remove(filepath.Join(s.dir, name))
				}
				release()
			}
			continue
		}
		key := strings.TrimSuffix(name, ".json")
		if len(key) != 64 {
			continue
		}
		release, err := s.lock(ctx, key)
		if err != nil {
			return err
		}
		func() {
			defer release()
			var r record
			if err := s.read(name, &r); err != nil {
				cleanupErr = err
				return
			}
			now := time.Now()
			if r.Key != key || r.Scope != h.scope || r.LastAccess > now.Add(-24*time.Hour).UnixMilli() || r.ExpiresAt > now.UnixMilli() {
				return
			}
			if r.DocumentID != "" {
				if !uuidPattern.MatchString(r.DocumentID) {
					cleanupErr = stateError()
					return
				}
				active, err := h.hasActiveShare(ctx, r.DocumentID)
				var remote *Error
				if active {
					return
				}
				if err != nil && !(errors.As(err, &remote) && remote.Code == "preview_remote_missing") {
					cleanupErr = err
					return
				}
				if err == nil {
					err = h.request(ctx, "DELETE", "/api/v1/documents/"+r.DocumentID, "", nil, nil)
				}
				if err != nil && !(errors.As(err, &remote) && remote.Code == "preview_remote_missing") {
					cleanupErr = err
					return
				}
			}
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				cleanupErr = err
			}
		}()
	}
	return cleanupErr
}
