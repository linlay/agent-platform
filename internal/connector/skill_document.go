package connector

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const MaxSkillDocumentBytes = 1 << 20

var ErrInvalidSkillTarget = errors.New("invalid connector skill target")
var ErrSkillDocumentTooLarge = errors.New("connector skill document exceeds 1 MiB")

type SkillDocument struct {
	Name      string
	Path      string
	Content   string
	SHA256    string
	Size      int64
	UpdatedAt int64
}

func (s Sources) SkillDocuments(id string) ([]SkillDocument, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	pkg, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	result := make([]SkillDocument, 0, len(pkg.Skills))
	for _, skill := range pkg.Skills {
		document, err := readSkillDocument(pkg, skill)
		if err != nil {
			return nil, err
		}
		result = append(result, document)
	}
	return result, nil
}

func (s Sources) ReadSkillDocument(id, name string) (SkillDocument, error) {
	if !ValidID(id) || !ValidID(name) {
		return SkillDocument{}, ErrInvalidSkillTarget
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	pkg, err := s.Load(id)
	if err != nil {
		return SkillDocument{}, err
	}
	for _, skill := range pkg.Skills {
		if skill.Name == name {
			return readSkillDocument(pkg, skill)
		}
	}
	return SkillDocument{}, os.ErrNotExist
}

func readSkillDocument(pkg Package, skill Skill) (SkillDocument, error) {
	rel, err := filepath.Rel(pkg.Dir, filepath.Join(skill.Dir, "SKILL.md"))
	if err != nil {
		return SkillDocument{}, err
	}
	// Constrain symlink resolution at open time, including package-local links.
	root, err := os.OpenRoot(pkg.Dir)
	if err != nil {
		return SkillDocument{}, err
	}
	defer root.Close()
	file, err := root.Open(rel)
	if err != nil {
		return SkillDocument{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return SkillDocument{}, err
	}
	if !info.Mode().IsRegular() {
		return SkillDocument{}, ErrInvalidSkillTarget
	}
	if info.Size() > MaxSkillDocumentBytes {
		return SkillDocument{}, ErrSkillDocumentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSkillDocumentBytes+1))
	if err != nil {
		return SkillDocument{}, err
	}
	if len(data) > MaxSkillDocumentBytes {
		return SkillDocument{}, ErrSkillDocumentTooLarge
	}
	if !utf8.Valid(data) {
		return SkillDocument{}, errors.New("connector skill document must be UTF-8")
	}
	return SkillDocument{Name: skill.Name, Path: filepath.ToSlash(rel), Content: string(data), SHA256: digest(data), Size: int64(len(data)), UpdatedAt: info.ModTime().UnixMilli()}, nil
}
