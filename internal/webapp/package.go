package webapp

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) Upload(ctx context.Context, reader io.Reader) (Upload, error) {
	result, dir, err := s.reserveUpload()
	if err != nil {
		return result, err
	}
	// The request body is read without mu: a slow upload must not stall page,
	// SDK or lifecycle traffic of running applications.
	keep := false
	defer func() {
		if !keep {
			_ = removeTree(dir)
		}
	}()
	f, err := os.OpenFile(filepath.Join(dir, "package.zip"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, s.maxArchive+1))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if n > s.maxArchive || n == 0 {
		return result, fail("webapp_archive_size_invalid", 413)
	}
	result.Size = n
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	result.CreatedAt = time.Now().UnixMilli()
	if err := writeJSON(filepath.Join(dir, "upload.json"), result); err != nil {
		return result, err
	}
	keep = true
	return result, nil
}

// reserveUpload counts an in-progress upload directory against the capacity
// before its body arrives; collection keeps directories younger than a day.
func (s *Service) reserveUpload() (Upload, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Upload
	if err := s.mutationError(); err != nil {
		return result, "", err
	}
	if err := s.collectUploads(); err != nil {
		return result, "", err
	}
	entries, err := os.ReadDir(s.uploads())
	if err != nil {
		return result, "", err
	}
	if len(entries) >= 128 {
		return result, "", fail("webapp_upload_capacity", 429)
	}
	free, err := availableBytes(s.root)
	if err != nil {
		return result, "", err
	}
	if free < uint64(s.maxArchive)+(32<<20) {
		return result, "", fail("webapp_disk_space_insufficient", 507)
	}
	result.UploadID = randomID()
	dir := filepath.Join(s.uploads(), result.UploadID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return result, "", err
	}
	return result, dir, nil
}
func (s *Service) uploads() string { return filepath.Join(s.root, ".staging", "uploads") }
func (s *Service) collectUploads() error {
	entries, err := os.ReadDir(s.uploads())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !opaquePattern.MatchString(e.Name()) {
			return fmt.Errorf("unexpected upload entry")
		}
		var u Upload
		dir := filepath.Join(s.uploads(), e.Name())
		if err := readJSON(filepath.Join(dir, "upload.json"), &u); err != nil {
			fi, statErr := e.Info()
			if statErr != nil {
				return statErr
			}
			if time.Since(fi.ModTime()) < 24*time.Hour {
				continue
			}
		} else if time.Now().UnixMilli()-u.CreatedAt < 24*time.Hour.Milliseconds() {
			continue
		}
		if err := removeTree(dir); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) readUpload(id string) (Upload, string, error) {
	var u Upload
	if !opaquePattern.MatchString(id) {
		return u, "", fail("webapp_upload_not_found", 404)
	}
	dir := filepath.Join(s.uploads(), id)
	if readJSON(filepath.Join(dir, "upload.json"), &u) != nil || u.UploadID != id || !isSHA256(u.SHA256) || time.Now().UnixMilli()-u.CreatedAt > 24*time.Hour.Milliseconds() {
		return u, "", fail("webapp_upload_not_found", 404)
	}
	name := filepath.Join(dir, "package.zip")
	fi, err := os.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != u.Size || fi.Size() > s.maxArchive {
		return u, "", fail("webapp_upload_changed", 409)
	}
	f, err := os.Open(name)
	if err != nil {
		return u, "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return u, "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != u.SHA256 {
		return u, "", fail("webapp_upload_changed", 409)
	}
	return u, name, nil
}
func (s *Service) Preflight(ctx context.Context, id string) (Review, error) {
	s.mu.Lock()
	err := s.mutationError()
	s.mu.Unlock()
	if err != nil {
		return Review{}, err
	}
	s.packageMu.Lock()
	defer s.packageMu.Unlock()
	return s.preflight(ctx, id)
}

// preflight runs under packageMu; mu is taken only to read the current
// installation revision.
func (s *Service) preflight(ctx context.Context, id string) (Review, error) {
	u, name, err := s.readUpload(id)
	if err != nil {
		return Review{}, err
	}
	dir, err := os.MkdirTemp(filepath.Join(s.root, ".staging"), "review-")
	if err != nil {
		return Review{}, err
	}
	defer removeTree(dir)
	m, total, count, err := s.extract(ctx, name, dir)
	if err != nil {
		return Review{}, err
	}
	revision := ""
	s.mu.Lock()
	if old := s.records[m.ID]; old != nil {
		revision = old.Revision
	}
	s.mu.Unlock()
	return Review{Upload: u, Manifest: m, ExpectedRevision: revision, ExpandedBytes: total, FileCount: count}, nil
}
func (s *Service) extract(ctx context.Context, archive, dest string) (Manifest, int64, int, error) {
	bad := func(code string) (Manifest, int64, int, error) { return Manifest{}, 0, 0, fail(code, 400) }
	z, err := zip.OpenReader(archive)
	if err != nil {
		return bad("webapp_archive_invalid")
	}
	defer z.Close()
	if len(z.File) == 0 || len(z.File) > s.maxFiles {
		return bad("webapp_file_limit_exceeded")
	}
	seen := map[string]bool{}
	root := ""
	manifestName := ""
	var declared uint64
	for _, f := range z.File {
		p := strings.TrimSuffix(f.Name, "/")
		if !safeRelative(p) || strings.ContainsAny(p, "\r\n\t") {
			return bad("webapp_archive_path_invalid")
		}
		key := strings.ToLower(p)
		if seen[key] {
			return bad("webapp_archive_path_collision")
		}
		seen[key] = true
		parts := strings.Split(p, "/")
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root || !idPattern.MatchString(root) {
			return bad("webapp_archive_root_invalid")
		}
		if f.Mode()&os.ModeType != 0 && !f.Mode().IsDir() {
			return bad("webapp_archive_link_forbidden")
		}
		if f.UncompressedSize64 > uint64(s.maxExpanded) || declared > uint64(s.maxExpanded)-f.UncompressedSize64 {
			return bad("webapp_expanded_size_exceeded")
		}
		declared += f.UncompressedSize64
		for _, p := range parts[1:] {
			l := strings.ToLower(p)
			if l == ".git" || l == "node_modules" || l == "__pycache__" || l == ".env" || strings.HasPrefix(l, ".env.") {
				return bad("webapp_package_path_forbidden")
			}
		}
		if p == root+"/webapp.json" {
			manifestName = f.Name
		}
	}
	if manifestName == "" {
		return bad("webapp_manifest_missing")
	}
	var total int64
	count := 0
	for _, f := range z.File {
		if ctx.Err() != nil {
			return Manifest{}, 0, 0, ctx.Err()
		}
		rel := strings.TrimPrefix(strings.TrimSuffix(f.Name, "/"), root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		name := filepath.Join(dest, filepath.FromSlash(rel))
		if f.Mode().IsDir() {
			if err := os.MkdirAll(name, 0700); err != nil {
				return Manifest{}, 0, 0, err
			}
			continue
		}
		if f.Mode()&os.ModeType != 0 {
			return bad("webapp_archive_link_forbidden")
		}
		ext := strings.ToLower(path.Ext(rel))
		switch ext {
		case ".node", ".dll", ".dylib", ".so", ".pyd", ".exe":
			return bad("webapp_native_artifact_forbidden")
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return Manifest{}, 0, 0, err
		}
		in, err := f.Open()
		if err != nil {
			return bad("webapp_archive_invalid")
		}
		out, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return Manifest{}, 0, 0, err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, s.maxExpanded-total+1))
		if copyErr == nil {
			copyErr = out.Sync()
		}
		out.Close()
		in.Close()
		if copyErr != nil {
			return bad("webapp_archive_invalid")
		}
		total += n
		count++
		if total > s.maxExpanded {
			return bad("webapp_expanded_size_exceeded")
		}
		probe, err := os.Open(name)
		if err != nil {
			return Manifest{}, 0, 0, err
		}
		magic := make([]byte, 4)
		nr, _ := probe.Read(magic)
		probe.Close()
		h := hex.EncodeToString(magic[:nr])
		if strings.HasPrefix(h, "7f454c46") || strings.HasPrefix(h, "4d5a") || h == "cafebabe" || h == "feedfacf" || h == "cffaedfe" || h == "cefaedfe" || h == "feedface" || h == "bebafeca" {
			return bad("webapp_native_artifact_forbidden")
		}
	}
	fi, err := os.Stat(filepath.Join(dest, "webapp.json"))
	if err != nil || fi.Size() > 256<<10 {
		return bad("webapp_manifest_invalid")
	}
	raw, err := os.ReadFile(filepath.Join(dest, "webapp.json"))
	if err != nil {
		return Manifest{}, 0, 0, err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return Manifest{}, 0, 0, err
	}
	if m.ID != root {
		return bad("webapp_archive_identity_mismatch")
	}
	if err := validateCompose(m); err != nil {
		return Manifest{}, 0, 0, err
	}
	files := []string{path.Join(m.Frontend.Root, m.Frontend.Index)}
	if p := m.Frontend.RouteConfig.NavigationFallback; p != "" {
		files = append(files, path.Join(m.Frontend.Root, p))
	}
	if m.Backend != nil {
		files = append(files, m.Backend.Command.Entry)
	}
	for _, p := range files {
		fi, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(p)))
		if err != nil || !fi.Mode().IsRegular() {
			return bad("webapp_package_entry_missing")
		}
	}
	return m, total, count, nil
}
