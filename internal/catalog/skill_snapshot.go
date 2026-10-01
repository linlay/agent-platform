package catalog

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type EditableSkillSnapshot struct {
	ID       string `json:"id"`
	Exists   bool   `json:"exists"`
	Revision string `json:"revision"`
	Archive  []byte `json:"archiveBase64,omitempty"`
}

func (r *FileRegistry) SnapshotEditableSkill(id string) (EditableSkillSnapshot, error) {
	r.skillPackageMu.Lock()
	defer r.skillPackageMu.Unlock()
	return snapshotEditableSkill(r.cfg.Paths.SkillsCenterDir, id)
}

// SnapshotSkill is only used while this mutation owns skillPackageMu, before
// Commit/Rollback. Acquiring it again would deadlock publication.
func (m *EditableSkillPackageMutation) SnapshotSkill(id string) (EditableSkillSnapshot, error) {
	return snapshotEditableSkill(m.root, id)
}

type boundedSkillZIP struct{ bytes.Buffer }

func (b *boundedSkillZIP) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > EditableSkillMaxUploadBytes {
		return 0, ErrSkillArchiveUploadTooLarge
	}
	return b.Buffer.Write(p)
}

func snapshotEditableSkill(root, id string) (EditableSkillSnapshot, error) {
	id = strings.TrimSpace(id)
	result := EditableSkillSnapshot{ID: id, Revision: "missing"}
	if strings.TrimSpace(root) == "" {
		return result, ErrInvalidSkillPath
	}
	dir, err := editableSkillDir(root, id)
	if errors.Is(err, ErrSkillNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, target := range []string{root, dir} {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return result, ErrSkillSymlink
		}
		if !info.IsDir() {
			return result, ErrInvalidSkillPath
		}
	}
	var buffer boundedSkillZIP
	writer := zip.NewWriter(&buffer)
	var total int64
	count := 0
	err = filepath.WalkDir(dir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		count++
		if count > EditableSkillMaxArchiveFiles {
			return ErrSkillArchiveTooManyFiles
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if strings.Contains(name, "\\") || strings.ContainsAny(name, "\x00\r\n") {
			return ErrInvalidSkillPath
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSkillSymlink
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return ErrInvalidSkillPath
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		header.SetMode(info.Mode())
		if info.IsDir() {
			header.Name += "/"
			header.Method = zip.Store
		}
		target, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Size() > EditableSkillMaxUploadBytes {
			return ErrSkillFileTooLarge
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return ErrSkillConflict
		}
		copied, err := io.Copy(target, io.LimitReader(file, EditableSkillMaxUploadBytes+1))
		if err != nil {
			return err
		}
		if copied > EditableSkillMaxUploadBytes {
			return ErrSkillFileTooLarge
		}
		total += copied
		if total > EditableSkillMaxArchiveBytes {
			return ErrSkillArchiveTooLarge
		}
		if copied != info.Size() {
			return ErrSkillConflict
		}
		return nil
	})
	closeErr := writer.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	result.Exists = true
	result.Archive = buffer.Bytes()
	result.Revision = fmt.Sprintf("%x", sha256.Sum256(result.Archive))
	return result, nil
}
