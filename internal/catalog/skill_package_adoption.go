package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Confirmation binds the approved old contents and the immutable incoming ZIP.
type SkillPackageAdoptionApproval struct {
	ArchiveSHA256     string            `json:"archiveSha256"`
	ExpectedRevisions map[string]string `json:"expectedRevisions"`
}
type SkillPackageAdoptionSkill struct {
	ID           string   `json:"id"`
	Revision     string   `json:"revision"`
	ChangedPaths []string `json:"changedPaths"`
}
type SkillPackageAdoptionConflict struct {
	ArchiveSHA256 string                      `json:"archiveSha256"`
	Skills        []SkillPackageAdoptionSkill `json:"skills"`
}

func (e *SkillPackageAdoptionConflict) Error() string {
	return "standalone skills differ from package; explicit replacement confirmation is required"
}
func (e *SkillPackageAdoptionConflict) Unwrap() error { return ErrSkillPackageConflict }

// Walk every entry, including dotfiles and modes, without following links. The
// full revision deliberately includes Desktop metadata even when comparison ignores it.
func skillAdoptionFiles(root string) (map[string]string, string, error) {
	entries := map[string]string{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
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
		if path == root {
			if !info.IsDir() {
				return ErrInvalidSkillPath
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(entries) >= EditableSkillMaxArchiveFiles {
			return ErrSkillArchiveTooManyFiles
		}
		if info.IsDir() {
			entries[rel] = "directory"
			return nil
		}
		if info.Size() > EditableSkillMaxUploadBytes {
			return ErrSkillFileTooLarge
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			file.Close()
			return ErrSkillConflict
		}
		hash := sha256.New()
		count, err := io.Copy(hash, io.LimitReader(file, EditableSkillMaxUploadBytes+1))
		file.Close()
		if err != nil {
			return err
		}
		total += count
		if total > EditableSkillMaxArchiveBytes {
			return ErrSkillArchiveTooLarge
		}
		if count != info.Size() {
			return ErrSkillConflict
		}
		entries[rel] = fmt.Sprintf("file:%o:%x", info.Mode().Perm()&0o111, hash.Sum(nil))
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(entries)
	return entries, fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

// Only the exact metadata shape written by Desktop may be ignored, and only
// when the package has no such file. Arbitrary skill.json contents stay protected.
func isDesktopSkillMetadata(path, id string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) != 5 {
		return false
	}
	for _, key := range []string{"id", "name", "version", "description"} {
		var value string
		if json.Unmarshal(object[key], &value) != nil {
			return false
		}
		if key == "id" && value != id {
			return false
		}
	}
	var tags []string
	return json.Unmarshal(object["tags"], &tags) == nil
}
func compareAdoptedSkill(existing, incoming, id string) (string, []string, error) {
	old, revision, err := skillAdoptionFiles(existing)
	if err != nil {
		return "", nil, err
	}
	next, _, err := skillAdoptionFiles(incoming)
	if err != nil {
		return "", nil, err
	}
	if _, exists := next["skill.json"]; !exists && isDesktopSkillMetadata(filepath.Join(existing, "skill.json"), id) {
		delete(old, "skill.json")
	}
	changed := map[string]bool{}
	for path, value := range old {
		if next[path] != value {
			changed[path] = true
		}
	}
	for path, value := range next {
		if old[path] != value {
			changed[path] = true
		}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return revision, paths, nil
}
