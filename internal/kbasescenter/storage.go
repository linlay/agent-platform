package kbasescenter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/config"
)

// configuration is the only on-disk source of desired state. ID comes from the directory.
type configuration struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Collections []Collection `json:"collections"`
}

type runtimeState struct {
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
	IndexedAt          int64  `json:"indexedAt"`
	State              string `json:"state"`
	Error              string `json:"error,omitempty"`
	AppliedFingerprint string `json:"appliedFingerprint,omitempty"`
	TaskFingerprint    string `json:"taskFingerprint,omitempty"`
}

func prepareRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("knowledge base root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return filepath.Abs(root)
}

func overlaps(a, b string) bool {
	inside := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return inside(a, b) || inside(b, a)
}

// Each runtime path component is checked separately; MkdirAll would follow substitutions.
func safeDirectory(root, child string, create bool) (string, error) {
	dir := filepath.Join(root, child)
	if create {
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
	}
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid knowledge base directory: %s", dir)
	}
	return dir, nil
}

func (s *Service) runtimeDirectory(id string, create bool) (string, error) {
	if !idPattern.MatchString(id) {
		return "", ErrNotFound
	}
	root, err := s.librariesDirectory(create)
	if err != nil {
		return "", err
	}
	return safeDirectory(root, id, create)
}

func (s *Service) librariesDirectory(create bool) (string, error) {
	runtimeRoot, err := safeDirectory(filepath.Dir(s.runtimeRoot), filepath.Base(s.runtimeRoot), create)
	if err != nil {
		return "", err
	}
	return safeDirectory(runtimeRoot, "libraries", create)
}

func readRegular(path string) ([]byte, os.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("expected regular file: %s", path)
	}
	b, err := os.ReadFile(path)
	return b, st, err
}

func (s *Service) loadConfiguration(id string, allowUnavailable bool) (Definition, error) {
	d := Definition{ID: id, Collections: []Collection{}}
	dir, err := s.directory(id)
	if err != nil {
		return d, err
	}
	b, info, err := readRegular(filepath.Join(dir, "library.yml"))
	if err != nil {
		return d, fmt.Errorf("library.yml is required (legacy library.json is unsupported): %w", err)
	}
	d.CreatedAt, d.UpdatedAt = info.ModTime().UnixMilli(), info.ModTime().UnixMilli()
	tree, err := config.LoadYAMLTreeBytesWithOptions(b, config.YAMLTreeOptions{
		RejectDuplicateKeys: true, DecodeDoubleQuotedEscapes: true,
		PreserveDecodedScalarPaths: []string{"name", "description", "collections.name", "collections.sourcePath"},
	})
	if err != nil {
		return d, fmt.Errorf("invalid library.yml: %w", err)
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return d, err
	}
	var desired configuration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&desired); err != nil {
		return d, fmt.Errorf("invalid library.yml: %w", err)
	}
	d.Name, d.Description, d.Collections = desired.Name, desired.Description, desired.Collections
	if err := validate(Input{Name: d.Name, Description: d.Description}); err != nil {
		return d, err
	}
	if err := validateCollections(d.Collections); err != nil {
		return d, err
	}
	for i, c := range d.Collections {
		source, err := s.canonicalSource(c.SourcePath)
		if err != nil {
			// Only existing unchanged sources may be offline during a metadata edit.
			// Refresh and all reads always require canonical, available sources.
			_, statErr := os.Stat(c.SourcePath)
			if allowUnavailable && os.IsNotExist(statErr) && !overlaps(filepath.Clean(c.SourcePath), s.root) && !overlaps(filepath.Clean(c.SourcePath), s.runtimeRoot) {
				d.Collections[i].SourcePath = filepath.Clean(c.SourcePath)
				continue
			}
			return d, fmt.Errorf("collection %s: %w", c.Name, err)
		}
		d.Collections[i].SourcePath = source
	}
	if err := validateCollections(d.Collections); err != nil {
		return d, err
	}
	return d, nil
}

func diagnostic(d Definition, id string, err error) Definition {
	d.ID = id
	if d.Name == "" {
		d.Name = id
	}
	if d.Collections == nil {
		d.Collections = []Collection{}
	}
	d.State, d.Error, d.IndexedAt = "error", err.Error(), 0
	return d
}

func (s *Service) load(id string) (Definition, error) {
	d, err := s.loadConfiguration(id, false)
	if err != nil {
		return d, err
	}
	state, err := s.readState(id)
	if err != nil {
		return d, err
	}
	if state.CreatedAt != 0 {
		d.CreatedAt = state.CreatedAt
	}
	if state.UpdatedAt > d.UpdatedAt {
		d.UpdatedAt = state.UpdatedAt
	}
	d.State, d.Error, d.IndexedAt = state.State, state.Error, state.IndexedAt
	if (d.State == "error" || d.State == "indexing") && !s.busy[id] && state.TaskFingerprint != "" && state.TaskFingerprint != scopeFingerprint(d.Collections) {
		d.State, d.Error, d.IndexedAt = "unindexed", "", 0
		return d, nil
	}
	if d.State == "indexing" {
		if !s.busy[id] {
			d = diagnostic(d, id, fmt.Errorf("indexing was interrupted; retry updating the index"))
		}
		return d, nil
	}
	if d.State == "error" {
		d.IndexedAt = 0
		return d, nil
	}
	// Missing runtime data or changed desired scope cannot masquerade as a ready index.
	if state.AppliedFingerprint != scopeFingerprint(d.Collections) || state.IndexedAt == 0 {
		d.State, d.Error, d.IndexedAt = "unindexed", "", 0
		return d, nil
	}
	dir, err := s.runtimeDirectory(id, false)
	if errors.Is(err, ErrNotFound) {
		d.State, d.IndexedAt = "unindexed", 0
		return d, nil
	}
	if err != nil {
		return d, err
	}
	st, err := os.Lstat(filepath.Join(dir, "index.sqlite"))
	if os.IsNotExist(err) {
		d.State, d.IndexedAt = "unindexed", 0
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if !st.Mode().IsRegular() {
		return d, fmt.Errorf("invalid KBX index file")
	}
	if d.State != "ready" {
		return d, fmt.Errorf("invalid index state")
	}
	return d, nil
}

func scopeFingerprint(collections []Collection) string {
	ordered := append([]Collection(nil), collections...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	raw, _ := json.Marshal(ordered)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func atomicWrite(dir, name string, b []byte) error {
	f, err := os.CreateTemp(dir, ".kbases-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}

func (s *Service) saveConfiguration(d Definition) error {
	dir, err := s.directory(d.ID)
	if err != nil {
		return err
	}
	// JSON quoting is a YAML-compatible scalar encoding, including paths and multiline text.
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\ndescription: %s\ncollections:\n", quote(d.Name), quote(d.Description))
	for _, c := range d.Collections {
		fmt.Fprintf(&b, "  - name: %s\n    sourcePath: %s\n", quote(c.Name), quote(c.SourcePath))
	}
	return atomicWrite(dir, "library.yml", []byte(b.String()))
}

func (s *Service) readState(id string) (runtimeState, error) {
	var state runtimeState
	dir, err := s.runtimeDirectory(id, false)
	if errors.Is(err, ErrNotFound) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	b, _, err := readRegular(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("invalid runtime state: %w", err)
	}
	return state, nil
}

func (s *Service) saveState(id string, state runtimeState) error {
	dir, err := s.runtimeDirectory(id, true)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(dir, "state.json", b)
}
