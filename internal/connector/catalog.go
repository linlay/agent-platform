package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Summary struct {
	Manifest
	Builtin   bool     `json:"builtin"`
	ReadOnly  bool     `json:"readOnly"`
	CanDelete bool     `json:"canDelete"`
	HasMCP    bool     `json:"hasMcp"`
	HasCLI    bool     `json:"hasCli"`
	HasBin    bool     `json:"hasBin"`
	Skills    []string `json:"skills"`
}

func Summaries(root string) ([]Summary, error) {
	return (Sources{ExternalRoot: root}).Summaries()
}

func (s Sources) Summaries() ([]Summary, error) {
	packages, err := s.LoadAll()
	if err != nil {
		return nil, err
	}
	result := make([]Summary, 0, len(packages))
	for _, pkg := range packages {
		summary := Summary{Manifest: pkg.Manifest, Builtin: pkg.Builtin, ReadOnly: pkg.Builtin, CanDelete: !pkg.Builtin, HasMCP: len(pkg.MCP) > 0, HasCLI: pkg.CLI != nil, HasBin: pkg.BinDir != "", Skills: []string{}}
		for _, skill := range pkg.Skills {
			summary.Skills = append(summary.Skills, skill.Name)
		}
		result = append(result, summary)
	}
	return result, nil
}

type File struct {
	ID      string `json:"id"`
	File    string `json:"file"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

func ReadFile(root, id, file string) (File, error) {
	if !ValidID(id) || !definitionFile(file) {
		return File{}, fmt.Errorf("invalid connector definition target")
	}
	if _, err := Load(root, id); err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(filepath.Join(root, id, file))
	if err != nil {
		return File{}, err
	}
	return File{ID: id, File: file, Content: string(data), SHA256: digest(data)}, nil
}

var mutationMu sync.Mutex
var ErrConflict = errors.New("connector definition changed; reload before saving")

// SaveDefinition keeps the optimistic concurrency check, local validation,
// publication and reload/rollback inside the same mutation boundary.
func SaveDefinition(root string, input File, expected string, validate func(Package) error, reload func() error) (File, error) {
	if IsBuiltin(input.ID) {
		return File{}, ErrBuiltinReadOnly
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	previous, err := ReadFile(root, input.ID, input.File)
	if err != nil {
		return File{}, err
	}
	if expected == "" || expected != previous.SHA256 {
		return File{}, ErrConflict
	}
	if len(input.Content) > 1<<20 {
		return File{}, fmt.Errorf("connector definition exceeds 1 MiB")
	}
	pkg, err := loadDefinition(root, input.ID, input.File, []byte(input.Content))
	if err == nil && validate != nil {
		err = validate(pkg)
	}
	if err != nil {
		return File{}, err
	}
	target := filepath.Join(root, input.ID, input.File)
	write := func(data []byte) error {
		file, err := os.CreateTemp(filepath.Dir(target), ".definition-")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return os.Rename(name, target)
	}
	if err := write([]byte(input.Content)); err != nil {
		return File{}, err
	}
	if reload != nil {
		err = reload()
	}
	if err != nil {
		if restoreErr := write([]byte(previous.Content)); restoreErr != nil {
			return File{}, fmt.Errorf("%v; restoring connector failed: %w", err, restoreErr)
		}
		if reload != nil {
			if restoreErr := reload(); restoreErr != nil {
				return File{}, fmt.Errorf("%v; restoring catalog failed: %w", err, restoreErr)
			}
		}
		return File{}, err
	}
	return ReadFile(root, input.ID, input.File)
}

func definitionFile(file string) bool {
	return file == "connector.json" || file == "mcp.json" || file == "cli.json"
}
func digest(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
