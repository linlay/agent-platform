package catalog

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type safeArchiveCandidate struct {
	file *zip.File
	path string
	dir  bool
	mode fs.FileMode
}

type safeArchiveEntry struct {
	file *zip.File
	path string
	dir  bool
	mode fs.FileMode
}

type safeArchivePolicy struct {
	subject         string
	maxFiles        int
	maxFileBytes    int64
	maxArchiveBytes int64
	tooManyFiles    error
	fileTooLarge    error
	archiveTooLarge error
	validationError func(code, message, sourcePath string) error
	directoryMode   func(relPath string) fs.FileMode
	parentMode      func(relPath string) fs.FileMode
	fileMode        func(relPath string, archiveMode fs.FileMode) fs.FileMode
	validateFile    func(pathOnDisk, relPath string) error
}

// collectSafeArchiveCandidates is the shared ZIP entry safety boundary used
// by both Skill and Agent imports. Layout markers such as SKILL.md or
// agent.yml remain the caller's responsibility.
func collectSafeArchiveCandidates(files []*zip.File, policy safeArchivePolicy) ([]safeArchiveCandidate, error) {
	if len(files) > policy.maxFiles {
		return nil, policy.tooManyFiles
	}
	candidates := make([]safeArchiveCandidate, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		rawPath := file.Name
		if rawPath == "" {
			return nil, policy.validationError("invalid_path", "ZIP entry path is empty", "")
		}
		if strings.Contains(rawPath, `\`) || strings.Contains(rawPath, "\x00") || path.IsAbs(rawPath) || filepath.IsAbs(rawPath) {
			return nil, policy.validationError("invalid_path", "ZIP entry path is not a safe relative path", rawPath)
		}
		isDir := file.FileInfo().IsDir() || strings.HasSuffix(rawPath, "/")
		trimmedPath := strings.TrimSuffix(rawPath, "/")
		if strings.TrimSpace(trimmedPath) != trimmedPath || trimmedPath == "" || path.Clean(trimmedPath) != trimmedPath {
			return nil, policy.validationError("invalid_path", "ZIP entry path is not normalized", rawPath)
		}
		if !validSafeArchiveRelativePath(trimmedPath) {
			return nil, policy.validationError("invalid_path", fmt.Sprintf("ZIP entry path escapes the %s directory", policy.subject), rawPath)
		}
		parts := strings.Split(trimmedPath, "/")
		if parts[0] == "__MACOSX" || path.Base(trimmedPath) == ".DS_Store" {
			continue
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, policy.validationError("symlink_not_allowed", "ZIP symlink entries are not allowed", rawPath)
		}
		if !isDir && !mode.IsRegular() {
			return nil, policy.validationError("unsupported_entry", "ZIP entry is not a regular file", rawPath)
		}
		candidates = append(candidates, safeArchiveCandidate{file: file, path: trimmedPath, dir: isDir, mode: mode})
	}
	if len(candidates) == 0 {
		return nil, policy.validationError("empty_archive", fmt.Sprintf("ZIP archive does not contain %s files", policy.subject), "")
	}
	return candidates, nil
}

func finalizeSafeArchiveEntries(candidates []safeArchiveCandidate, prefix string, policy safeArchivePolicy) ([]safeArchiveEntry, error) {
	entries := make([]safeArchiveEntry, 0, len(candidates))
	seen := map[string]string{}
	kinds := map[string]bool{}
	var totalSize uint64
	for _, candidate := range candidates {
		relPath := candidate.path
		if prefix != "" {
			if relPath == strings.TrimSuffix(prefix, "/") && candidate.dir {
				continue
			}
			if !strings.HasPrefix(relPath, prefix) {
				return nil, policy.validationError("invalid_layout", fmt.Sprintf("ZIP contains entries outside its top-level %s directory", policy.subject), candidate.path)
			}
			relPath = strings.TrimPrefix(relPath, prefix)
		}
		if !validSafeArchiveRelativePath(relPath) {
			return nil, policy.validationError("invalid_path", fmt.Sprintf("ZIP entry path escapes the %s directory", policy.subject), candidate.path)
		}
		folded := strings.ToLower(relPath)
		if previous, ok := seen[folded]; ok {
			return nil, policy.validationError("duplicate_path", fmt.Sprintf("ZIP contains conflicting paths %q and %q", previous, relPath), relPath)
		}
		seen[folded] = relPath
		kinds[folded] = candidate.dir
		if !candidate.dir {
			if candidate.file.UncompressedSize64 > uint64(policy.maxFileBytes) {
				return nil, policy.fileTooLarge
			}
			if candidate.file.UncompressedSize64 > uint64(policy.maxArchiveBytes)-totalSize {
				return nil, policy.archiveTooLarge
			}
			totalSize += candidate.file.UncompressedSize64
		}
		entries = append(entries, safeArchiveEntry{file: candidate.file, path: relPath, dir: candidate.dir, mode: candidate.mode})
	}
	for folded, relPath := range seen {
		parts := strings.Split(folded, "/")
		for index := 1; index < len(parts); index++ {
			parent := strings.Join(parts[:index], "/")
			if isDir, ok := kinds[parent]; ok && !isDir {
				return nil, policy.validationError("path_conflict", fmt.Sprintf("ZIP file %q conflicts with a child path", seen[parent]), relPath)
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].dir != entries[j].dir {
			return entries[i].dir
		}
		return entries[i].path < entries[j].path
	})
	return entries, nil
}

func extractSafeArchiveEntry(stagingDir string, entry safeArchiveEntry, remainingArchiveBytes int64, policy safeArchivePolicy) (int64, error) {
	target := filepath.Join(stagingDir, filepath.FromSlash(entry.path))
	if !insideDir(stagingDir, target) {
		return 0, policy.validationError("invalid_path", fmt.Sprintf("ZIP entry path escapes the %s directory", policy.subject), entry.path)
	}
	if entry.dir {
		mode := policy.directoryMode(entry.path)
		if err := os.MkdirAll(target, mode); err != nil {
			return 0, err
		}
		return 0, os.Chmod(target, mode)
	}
	if err := os.MkdirAll(filepath.Dir(target), policy.parentMode(entry.path)); err != nil {
		return 0, err
	}
	input, err := entry.file.Open()
	if err != nil {
		return 0, policy.validationError("corrupt_entry", "ZIP entry cannot be opened", entry.path)
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, policy.validationError("duplicate_path", "ZIP entry path is duplicated", entry.path)
		}
		return 0, err
	}
	writeLimit := policy.maxFileBytes
	if remainingArchiveBytes < writeLimit {
		writeLimit = remainingArchiveBytes
	}
	limited := &io.LimitedReader{R: input, N: writeLimit + 1}
	written, copyErr := io.Copy(output, limited)
	closeErr := output.Close()
	if copyErr != nil {
		return 0, policy.validationError("corrupt_entry", "ZIP entry could not be extracted", entry.path)
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if written > remainingArchiveBytes {
		return 0, policy.archiveTooLarge
	}
	if written > policy.maxFileBytes {
		return 0, policy.fileTooLarge
	}
	if uint64(written) != entry.file.UncompressedSize64 {
		return 0, policy.validationError("corrupt_entry", "ZIP entry size does not match its header", entry.path)
	}
	if err := os.Chmod(target, policy.fileMode(entry.path, entry.mode)); err != nil {
		return 0, err
	}
	if policy.validateFile != nil {
		if err := policy.validateFile(target, filepath.ToSlash(entry.path)); err != nil {
			return 0, policy.validationError("invalid_special_file", err.Error(), entry.path)
		}
	}
	return written, nil
}

func validSafeArchiveRelativePath(relPath string) bool {
	if relPath == "" || relPath == "." || strings.HasPrefix(relPath, "/") {
		return false
	}
	clean := path.Clean(relPath)
	return clean == relPath && clean != ".." && !strings.HasPrefix(clean, "../")
}
