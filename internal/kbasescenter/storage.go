package kbasescenter

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/pathutil"
)

// configuration is the only on-disk source of desired state. ID comes from the directory.
type configuration struct {
	Retrieval    *knowledge.RetrievalSettings `json:"retrieval,omitempty"`
	Chunk        *knowledge.ChunkSettings     `json:"chunk,omitempty"`
	TextEncoding string                       `json:"textEncoding,omitempty"`
	Models       *ModelsConfig                `json:"models,omitempty"`
	Name         string                       `json:"name"`
	Description  string                       `json:"description"`
	Collections  []Collection                 `json:"collections"`
}

var errInvalidRuntimeState = errors.New("invalid runtime state")

type runtimeState struct {
	CreatedAt                int64  `json:"createdAt"`
	UpdatedAt                int64  `json:"updatedAt"`
	IndexedAt                int64  `json:"indexedAt"`
	State                    string `json:"state"`
	Error                    string `json:"error,omitempty"`
	RefreshError             string `json:"refreshError,omitempty"`
	AppliedVectorFingerprint string `json:"appliedVectorFingerprint,omitempty"`
	TaskVectorFingerprint    string `json:"taskVectorFingerprint,omitempty"`
	VectorOnlyTask           bool   `json:"vectorOnlyTask,omitempty"`
	AppliedFingerprint       string `json:"appliedFingerprint,omitempty"`
	TaskFingerprint          string `json:"taskFingerprint,omitempty"`
	Degraded                 bool   `json:"degraded,omitempty"`
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
	left := pathutil.Canonical{Key: pathutil.CanonicalKey(a)}
	right := pathutil.Canonical{Key: pathutil.CanonicalKey(b)}
	return pathutil.WithinRoot(left, right) || pathutil.WithinRoot(right, left)
}

// Only a live YAML definition or a diagnosable legacy definition identifies a library.
// A template-only directory must never be editable or deletable through the library API.
func hasDefinition(dir string) (bool, error) {
	for _, name := range []string{"library.yml", "library.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func quarantineDirectory(dir string) (string, error) {
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	trash := filepath.Join(filepath.Dir(dir), ".deleted-"+filepath.Base(dir)+"-"+hex.EncodeToString(token))
	if err := os.Rename(dir, trash); err != nil {
		return "", err
	}
	return trash, nil
}

func quarantineAndRemove(dir string) error {
	trash, err := quarantineDirectory(dir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("remove quarantined knowledge base %s: %w", trash, err)
	}
	return nil
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
	if !ValidID(id) {
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
	return runtimeRoot, nil
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
		RejectDuplicateKeys: true, DecodeDoubleQuotedEscapes: true, DecodeSingleQuotedEscapes: true, DisableEnvInterpolation: true,
	})
	if err != nil {
		return d, yamlConfigurationError(err)
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return d, err
	}
	var desired configuration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&desired); err != nil {
		return d, yamlConfigurationError(err)
	}
	d.Name, d.Description, d.Collections = desired.Name, desired.Description, desired.Collections
	d.Chunk, d.TextEncoding, d.Models = desired.Chunk, desired.TextEncoding, desired.Models
	d.Retrieval = desired.Retrieval
	if normalized, err := normalizeTextEncoding(d.TextEncoding); err == nil {
		d.TextEncoding = normalized
	}
	if err := validateLibrarySettings(d); err != nil {
		return d, err
	}
	if err := validate(Input{Name: d.Name, Description: d.Description}); err != nil {
		return d, err
	}
	if err := validateCollections(d.Collections); err != nil {
		return d, err
	}
	for i, c := range d.Collections {
		source, err := s.canonicalSource(c.SourcePath)
		if err != nil {
			_, statErr := os.Stat(c.SourcePath)
			if allowUnavailable && statErr != nil {
				resolved, offlineErr := s.offlineSource(c)
				if offlineErr != nil {
					return d, fmt.Errorf("collection %s: %w", c.Name, offlineErr)
				}
				d.Collections[i].SourcePath = resolved
				d.SourceWarnings = append(d.SourceWarnings, fmt.Sprintf("collection %s: source directory is unavailable; serving the last completed index when its scope matches", c.Name))
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
	d, err := s.loadConfiguration(id, true)
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
	d.RefreshError = state.RefreshError
	d.Indexing = s.isBusy(id)
	d.Stale = d.Indexing || state.RefreshError != ""
	fingerprints := s.fingerprints(d)
	d.VectorsPending = fingerprints.Vector != state.AppliedVectorFingerprint || state.VectorOnlyTask
	d.Degraded = state.Degraded || d.VectorsPending
	if state.VectorOnlyTask && state.AppliedFingerprint == fingerprints.Source && state.IndexedAt > 0 {
		// A vector task never withdraws the committed full-text index, including
		// after interruption. The scheduler will reconcile it after restart.
		if !s.isBusy(id) && state.State == "indexing" {
			d.RefreshError = "vector rebuild was interrupted; automatic reconciliation will retry"
			d.Degraded = true
		}
		d.State = "ready"
		d.Stale = d.Stale || d.Degraded
	}

	if (d.State == "error" || d.State == "indexing") && !s.isBusy(id) && state.TaskFingerprint != "" && state.TaskFingerprint != fingerprints.Source {
		d.State, d.Error, d.IndexedAt = "unindexed", "", 0
		return d, nil
	}
	if d.State == "indexing" {
		if !s.isBusy(id) {
			d.IndexedAt = 0
			d.State = "error"
			d.Error = "indexing was interrupted; automatic reconciliation will retry"
			d.Stale = true
			return d, nil
		}
		if state.AppliedFingerprint != fingerprints.Source {
			d.IndexedAt = 0
		}
		return d, nil
	}
	if d.State == "error" {
		d.IndexedAt = 0
		return d, nil
	}
	// Missing runtime data or changed desired scope cannot masquerade as a ready index.
	if state.AppliedFingerprint != fingerprints.Source || state.IndexedAt == 0 {
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

func (s *Service) fingerprint(collections []Collection) string {
	return scopeFingerprint(s.effectiveCollections(collections))
}
func scopeFingerprint(collections []Collection) string {
	// Explicit projection preserves the existing fingerprint encoding and excludes
	// Run-only metadata. New fields must choose a fingerprint category deliberately.
	type sourceCollection struct {
		Name       string                  `json:"name"`
		SourcePath string                  `json:"sourcePath"`
		Include    []string                `json:"include"`
		Exclude    []string                `json:"exclude"`
		Chunk      knowledge.ChunkSettings `json:"chunk,omitempty"`
	}
	ordered := make([]sourceCollection, 0, len(collections))
	for _, c := range effectiveCollections(collections) {
		ordered = append(ordered, sourceCollection{c.Name, c.SourcePath, c.Include, c.Exclude, c.Chunk})
	}
	for i := range ordered {
		ordered[i].SourcePath = pathutil.CanonicalKey(ordered[i].SourcePath)
	}
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
	dir, err := s.rawDirectory(d.ID)
	if err != nil {
		return err
	}
	// JSON quoting is a YAML-compatible scalar encoding, including paths and multiline text.
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\ndescription: %s\n", quote(d.Name), quote(d.Description))
	writeLibrarySettings(&b, d, quote)
	fmt.Fprintln(&b, "collections:")
	for _, c := range d.Collections {
		fmt.Fprintf(&b, "  - name: %s\n    sourcePath: %s\n", quote(c.Name), quote(c.SourcePath))
		if c.Description != "" {
			fmt.Fprintf(&b, "    description: %s\n", quote(c.Description))
		}
		if c.Editable {
			fmt.Fprintln(&b, "    editable: true")
		}
		for _, field := range []struct {
			name   string
			values []string
		}{{"include", c.Include}, {"exclude", c.Exclude}} {
			if field.values != nil {
				if len(field.values) == 0 {
					fmt.Fprintf(&b, "    %s: []\n", field.name)
				} else {
					fmt.Fprintf(&b, "    %s:\n", field.name)
					for _, v := range field.values {
						fmt.Fprintf(&b, "      - %s\n", quote(v))
					}
				}
			}
		}
		if c.DefaultQuery != nil {
			fmt.Fprintf(&b, "    defaultQuery: %t\n", *c.DefaultQuery)
		}
		writeChunkSettings(&b, "    ", c.Chunk, quote)
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
		return state, fmt.Errorf("%w: %v", errInvalidRuntimeState, err)
	}
	switch state.State {
	case "unindexed", "indexing", "ready", "error":
	default:
		return state, errInvalidRuntimeState
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

func yamlConfigurationError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field == "collections" && typeErr.Type.Kind().String() == "slice" {
			return fmt.Errorf("invalid library.yml: collections must use a block list (one '- name:' entry per collection); inline collections: [{...}] is unsupported")
		}
		if typeErr.Type.Kind().String() != "string" {
			return fmt.Errorf("invalid library.yml: %s must be %s", typeErr.Field, typeErr.Type)
		}
		return fmt.Errorf("invalid library.yml: %s must be text; enclose the value in double quotes (for example name: \"2024\")", typeErr.Field)
	}
	return fmt.Errorf("invalid library.yml: %w", err)
}

func (s *Service) offlineSource(c Collection) (string, error) {
	// Resolve existing ancestors and links even when the leaf is offline. Never
	// reuse an index after a link has been redirected: load compares the resolved
	// scope against the fingerprint saved by the last successful update.
	canonical, err := pathutil.Canonicalize(c.SourcePath)
	if err != nil {
		return "", err
	}
	if err := s.validateSource(canonical.Host); err != nil {
		return "", err
	}
	return canonical.Host, nil
}

var quarantineName = regexp.MustCompile(`^\.deleted-[a-z0-9][a-z0-9_-]{0,63}-[a-f0-9]{24}$`)

// Startup only removes our generated quarantine directories, never arbitrary
// hidden files, template directories, or links to external content.
func cleanupQuarantines(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("[kbases] read quarantine directory %s: %v", root, err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !quarantineName.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			log.Printf("[kbases] cleanup quarantine %s: %v", entry.Name(), err)
		}
	}
}
