package connector

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const MaxArchiveUploadBytes int64 = 64 << 20
const maxArchiveBytes int64 = 256 << 20

var ErrArchiveTooLarge = errors.New("connector ZIP exceeds size or file count limits")
var ErrPackageExists = errors.New("connector already installed; overwrite is required")

// ImportArchive publishes a fully validated package while sharing the same
// mutation boundary as definition edits. Network availability is not validation.
func ImportArchive(ctx context.Context, sources Sources, source io.ReaderAt, size int64, overwrite bool, validate func([]Package) error, reload func() error) (Package, error) {
	if size <= 0 || size > MaxArchiveUploadBytes {
		return Package{}, ErrArchiveTooLarge
	}
	zr, err := zip.NewReader(source, size)
	if err != nil {
		return Package{}, fmt.Errorf("invalid connector ZIP: %w", err)
	}
	if len(zr.File) > 8192 {
		return Package{}, ErrArchiveTooLarge
	}
	root, err := filepath.Abs(sources.ExternalRoot)
	if err != nil || strings.TrimSpace(sources.ExternalRoot) == "" {
		return Package{}, fmt.Errorf("connector root is required")
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Package{}, err
	}
	stage, err := os.MkdirTemp(root, ".connector-import-")
	if err != nil {
		return Package{}, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	files := make(map[string]*zip.File)
	prefix := ""
	manifestCount := 0
	var total uint64
	spellings := map[string]string{}
	for _, f := range zr.File {
		n := strings.TrimSuffix(f.Name, "/")
		if !safeArchivePath(n) {
			return Package{}, fmt.Errorf("unsafe ZIP path %q", f.Name)
		}
		if strings.HasPrefix(n, "__MACOSX/") || n == "__MACOSX" || path.Base(n) == ".DS_Store" {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return Package{}, fmt.Errorf("unsupported ZIP entry %q", n)
		}
		fold := strings.ToLower(n)
		if _, ok := files[fold]; ok {
			return Package{}, fmt.Errorf("duplicate ZIP path %q", n)
		}
		files[fold] = f
		for p := n; p != "."; p = path.Dir(p) {
			key := strings.ToLower(p)
			if prior, exists := spellings[key]; exists && prior != p {
				return Package{}, fmt.Errorf("case-conflicting ZIP paths %q and %q", prior, p)
			}
			spellings[key] = p
		}
		if !f.FileInfo().IsDir() {
			if f.UncompressedSize64 > 128<<20 || f.UncompressedSize64 > uint64(maxArchiveBytes)-total {
				return Package{}, ErrArchiveTooLarge
			}
			total += f.UncompressedSize64
		}
		if path.Base(n) == "connector.json" && !f.FileInfo().IsDir() {
			manifestCount++
			prefix = strings.TrimSuffix(n, "connector.json")
		}
	}
	if manifestCount != 1 || strings.Count(strings.TrimSuffix(prefix, "/"), "/") > 0 {
		return Package{}, fmt.Errorf("ZIP requires one connector.json at root or inside one top-level directory")
	}
	manifestFile := files[strings.ToLower(prefix+"connector.json")]
	reader, err := manifestFile.Open()
	if err != nil {
		return Package{}, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	reader.Close()
	if err != nil {
		return Package{}, err
	}
	if len(data) > 1<<20 {
		return Package{}, ErrArchiveTooLarge
	}
	var manifest Manifest
	if err := DecodeJSON(data, &manifest); err != nil {
		return Package{}, err
	}
	if err := validateManifest(manifest.ID, manifest); err != nil {
		return Package{}, err
	}
	if IsBuiltin(manifest.ID) {
		return Package{}, ErrBuiltinReadOnly
	}
	if prefix != "" && strings.TrimSuffix(prefix, "/") != manifest.ID {
		return Package{}, fmt.Errorf("ZIP directory must match connector id")
	}
	release, err := AcquireOperation(root, manifest.ID)
	if err != nil {
		return Package{}, err
	}
	defer release()
	candidate := filepath.Join(stage, manifest.ID)
	if err := os.Mkdir(candidate, 0o755); err != nil {
		return Package{}, err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Package{}, err
		}
		n := strings.TrimSuffix(f.Name, "/")
		if prefix != "" && n == strings.TrimSuffix(prefix, "/") && f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(n, prefix) {
			return Package{}, fmt.Errorf("ZIP contains files outside connector directory")
		}
		rel := strings.TrimPrefix(n, prefix)
		if !safeArchivePath(rel) {
			return Package{}, fmt.Errorf("invalid ZIP layout")
		}
		if strings.HasPrefix(strings.Split(rel, "/")[0], ".") {
			return Package{}, fmt.Errorf("hidden package root entries are reserved")
		}
		// Reject file/directory and case-insensitive ancestor collisions on all OSes.
		for parent := path.Dir(n); parent != "."; parent = path.Dir(parent) {
			if p := files[strings.ToLower(parent)]; p != nil && (!p.FileInfo().IsDir() || strings.TrimSuffix(p.Name, "/") != parent) {
				return Package{}, fmt.Errorf("conflicting ZIP parent %q", parent)
			}
		}
		target := filepath.Join(candidate, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return Package{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Package{}, err
		}
		in, err := f.Open()
		if err != nil {
			return Package{}, err
		}
		mode := os.FileMode(0o644)
		if f.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			in.Close()
			return Package{}, err
		}
		count, copyErr := io.Copy(out, io.LimitReader(in, int64(f.UncompressedSize64)+1))
		closeErr := out.Close()
		in.Close()
		if copyErr != nil {
			return Package{}, copyErr
		}
		if closeErr != nil {
			return Package{}, closeErr
		}
		if count != int64(f.UncompressedSize64) {
			return Package{}, fmt.Errorf("ZIP entry size mismatch")
		}
	}
	pkg, err := Load(stage, manifest.ID)
	if err != nil {
		return Package{}, err
	}
	installed, err := sources.loadAllExcept(pkg.ID)
	if err != nil {
		return Package{}, err
	}
	var all []Package
	for _, old := range installed {
		if old.ID != pkg.ID {
			all = append(all, old)
		}
	}
	all = append(all, pkg)
	if validate != nil {
		if err := validate(all); err != nil {
			return Package{}, err
		}
	}
	target := filepath.Join(root, pkg.ID)
	backup := filepath.Join(stage, "previous")
	hadPrevious := false
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Package{}, fmt.Errorf("connector target is not a real directory")
		}
		if !overwrite {
			return Package{}, ErrPackageExists
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return Package{}, err
	}
	if err := ctx.Err(); err != nil {
		return Package{}, err
	}
	if hadPrevious {
		if err := os.Rename(target, backup); err != nil {
			return Package{}, err
		}
	}
	if err := os.Rename(candidate, target); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				keepStage = true
				return Package{}, fmt.Errorf("install: %v; backup retained at %s: %w", err, backup, restoreErr)
			}
		}
		return Package{}, err
	}
	if reload != nil {
		err = reload()
	}
	if err != nil {
		if restoreErr := os.Rename(target, candidate); restoreErr != nil {
			keepStage = true
			return Package{}, fmt.Errorf("reload: %v; cannot move failed package: %w", err, restoreErr)
		}
		if hadPrevious {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				keepStage = true
				return Package{}, fmt.Errorf("reload: %v; cannot restore backup %s: %w", err, backup, restoreErr)
			}
		}
		if reload != nil {
			if restoreErr := reload(); restoreErr != nil {
				return Package{}, fmt.Errorf("reload: %v; reload restored catalog: %w", err, restoreErr)
			}
		}
		return Package{}, err
	}
	return Load(root, pkg.ID)
}

func safeArchivePath(n string) bool {
	if n == "" || n == "." || path.IsAbs(n) || path.Clean(n) != n || strings.ContainsAny(n, "\\\x00:") {
		return false
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return false
		}
	}
	return true
}
