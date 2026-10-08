// Package kbasescenter owns deployment-level knowledge libraries independently of Agents.
package kbasescenter

import (
	"context"
	"crypto/rand"
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
	"sync"
	"time"
)

var ErrNotFound = errors.New("knowledge base not found")
var ErrBusy = errors.New("knowledge base is indexing")
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var collectionNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_-]{0,63}$`)

type Collection struct {
	Name       string `json:"name"`
	SourcePath string `json:"sourcePath"`
}

type Definition struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Collections []Collection `json:"collections"`
	// SourcePath is a convenience field for the HTTP single-source input.
	SourcePath string `json:"sourcePath,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	IndexedAt  int64  `json:"indexedAt"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	Orphaned   bool   `json:"orphaned,omitempty"`
}
type Input struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Collections []Collection `json:"collections,omitempty"`
	SourcePath  string       `json:"sourcePath,omitempty"`
}
type SearchInput struct {
	Query       string   `json:"query"`
	Limit       int      `json:"limit"`
	Collections []string `json:"collections,omitempty"`
	Method      string   `json:"method,omitempty"`
}

// Engine accepts only Platform-owned database paths and verified source directories.
type Engine interface {
	Update(context.Context, string, []Collection) error
	Read(context.Context, string, string, string, int, ...string) (json.RawMessage, error)
}
type Service struct {
	root        string
	runtimeRoot string // ru-kbases; libraries/ is reserved for independent libraries.
	engine      Engine
	ctx         context.Context
	mu          sync.RWMutex
	busy        map[string]bool
}

func New(ctx context.Context, root, runtimeRoot string, engine Engine) (*Service, error) {
	var err error
	root, err = prepareRoot(root)
	if err != nil {
		return nil, err
	}
	runtimeRoot, err = prepareRoot(runtimeRoot)
	if err != nil {
		return nil, err
	}
	if overlaps(root, runtimeRoot) {
		return nil, fmt.Errorf("knowledge base configuration and runtime roots must not overlap")
	}
	if _, err = safeDirectory(runtimeRoot, "libraries", true); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Service{root: root, runtimeRoot: runtimeRoot, engine: engine, ctx: ctx, busy: map[string]bool{}}, nil
}
func (s *Service) directory(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", ErrNotFound
	}
	root, err := safeDirectory(filepath.Dir(s.root), filepath.Base(s.root), false)
	if err != nil {
		return "", err
	}
	return safeDirectory(root, id, false)
}
func validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 {
		return fmt.Errorf("name is required and must be at most 200 bytes")
	}
	if len(in.Description) > 4000 {
		return fmt.Errorf("description must be at most 4000 bytes")
	}
	return nil
}

func validateCollections(collections []Collection) error {
	if len(collections) < 1 || len(collections) > 32 {
		return fmt.Errorf("a knowledge base must contain 1–32 collections")
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for _, c := range collections {
		if !collectionNamePattern.MatchString(c.Name) {
			return fmt.Errorf("collection names must contain 1–64 letters, digits, underscores or hyphens, starting with a letter or digit")
		}
		if names[c.Name] {
			return fmt.Errorf("duplicate collection name: %s", c.Name)
		}
		if !filepath.IsAbs(c.SourcePath) {
			return fmt.Errorf("collection sourcePath must be an absolute directory path on the Platform host")
		}
		if paths[c.SourcePath] {
			return fmt.Errorf("each collection must use a distinct source directory")
		}
		names[c.Name], paths[c.SourcePath] = true, true
	}
	return nil
}

// DocumentReference splits KBX's virtual reference without flattening its source path.
func DocumentReference(ref string) (collection, path string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "kbx://")
	if !ok {
		return "", "", false
	}
	collection, path, ok = strings.Cut(rest, "/")
	if !ok || !collectionNamePattern.MatchString(collection) || path == "" || strings.ContainsAny(path, "\\\x00") {
		return "", "", false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", false
		}
	}
	return collection, path, true
}

func (s *Service) canonicalSource(source string) (string, error) {
	if !filepath.IsAbs(source) {
		return "", fmt.Errorf("sourcePath must be an absolute directory path on the Platform host")
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("source directory is unavailable")
	}
	st, err := os.Stat(source)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("sourcePath must be an existing directory")
	}
	if overlaps(source, s.root) || overlaps(source, s.runtimeRoot) {
		return "", fmt.Errorf("source directory must not overlap kbases or ru-kbases")
	}
	return source, nil
}
func (s *Service) List() ([]Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	root, err := safeDirectory(filepath.Dir(s.root), filepath.Base(s.root), false)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := []Definition{}
	seen := map[string]bool{}
	for _, entry := range entries {
		id := entry.Name()
		if !idPattern.MatchString(id) || (!entry.IsDir() && entry.Type()&os.ModeSymlink == 0) {
			continue
		}
		// Templates are not live definitions. Legacy JSON is explicitly diagnosed.
		_, yamlErr := os.Lstat(filepath.Join(s.root, id, "library.yml"))
		_, legacyErr := os.Lstat(filepath.Join(s.root, id, "library.json"))
		if os.IsNotExist(yamlErr) && os.IsNotExist(legacyErr) {
			continue
		}
		seen[id] = true
		d, err := s.load(id)
		if err != nil {
			d = diagnostic(d, id, err)
		}
		out = append(out, d)
	}
	libraries, err := s.librariesDirectory(false)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	entries = nil
	if err == nil {
		entries, err = os.ReadDir(libraries)
		if err != nil {
			return nil, err
		}
	}
	for _, entry := range entries {
		id := entry.Name()
		if !idPattern.MatchString(id) || seen[id] || (!entry.IsDir() && entry.Type()&os.ModeSymlink == 0) {
			continue
		}
		d := diagnostic(Definition{Orphaned: true}, id, fmt.Errorf("orphaned runtime data: restore kbases/%s/library.yml or explicitly delete this library", id))
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s *Service) Get(id string) (Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.load(id)
	if err != nil {
		if dir, dirErr := s.directory(id); dirErr == nil {
			_, yamlErr := os.Lstat(filepath.Join(dir, "library.yml"))
			_, legacyErr := os.Lstat(filepath.Join(dir, "library.json"))
			if _, runErr := s.runtimeDirectory(id, false); runErr == nil && os.IsNotExist(yamlErr) && os.IsNotExist(legacyErr) {
				d.Orphaned = true
			}
			return diagnostic(d, id, err), nil
		}
		if _, runErr := s.runtimeDirectory(id, false); runErr == nil && errors.Is(err, ErrNotFound) {
			return diagnostic(Definition{Orphaned: true}, id, fmt.Errorf("orphaned runtime data: configuration is missing")), nil
		}
	}
	return d, err
}
func (s *Service) Create(in Input) (Definition, error) {
	if err := validate(in); err != nil {
		return Definition{}, err
	}
	if in.SourcePath != "" && in.Collections != nil {
		return Definition{}, fmt.Errorf("sourcePath and collections are mutually exclusive")
	}
	collections := append([]Collection(nil), in.Collections...)
	if in.SourcePath != "" {
		collections = []Collection{{Name: "workspace", SourcePath: in.SourcePath}}
	}
	if err := validateCollections(collections); err != nil {
		return Definition{}, err
	}
	for i := range collections {
		source, err := s.canonicalSource(collections[i].SourcePath)
		if err != nil {
			return Definition{}, fmt.Errorf("collection %s: %w", collections[i].Name, err)
		}
		collections[i].SourcePath = source
	}
	if err := validateCollections(collections); err != nil {
		return Definition{}, err
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return Definition{}, err
	}
	now := time.Now().UnixMilli()
	d := Definition{ID: hex.EncodeToString(b), Name: strings.TrimSpace(in.Name), Description: in.Description, Collections: collections, CreatedAt: now, UpdatedAt: now, State: "unindexed"}
	if in.SourcePath != "" {
		d.SourcePath = collections[0].SourcePath
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := safeDirectory(filepath.Dir(s.root), filepath.Base(s.root), false)
	if err != nil {
		return Definition{}, err
	}
	dir := filepath.Join(root, d.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return Definition{}, err
	}
	if err := s.saveConfiguration(d); err != nil {
		os.RemoveAll(dir)
		return Definition{}, err
	}
	if err := s.saveState(d.ID, runtimeState{CreatedAt: now, UpdatedAt: now, State: "unindexed"}); err != nil {
		os.RemoveAll(dir)
		return Definition{}, err
	}
	return d, nil
}
func (s *Service) Edit(id string, in Input) (Definition, error) {
	if err := validate(in); err != nil {
		return Definition{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return Definition{}, ErrBusy
	}
	if _, err := s.directory(id); err != nil {
		return Definition{}, err
	}
	d, err := s.loadConfiguration(id, true)
	if err != nil {
		// A full valid replacement can repair malformed YAML through the admin UI.
		if in.Collections == nil && in.SourcePath == "" {
			return d, err
		}
		d = Definition{ID: id}
	}
	if in.SourcePath != "" && in.Collections != nil {
		return d, fmt.Errorf("sourcePath and collections are mutually exclusive")
	}
	collections := append([]Collection(nil), in.Collections...)
	if in.SourcePath != "" {
		collections = []Collection{{Name: "workspace", SourcePath: in.SourcePath}}
	}
	if in.Collections != nil || in.SourcePath != "" {
		if err := validateCollections(collections); err != nil {
			return d, err
		}
		existing := map[string]string{}
		for _, c := range d.Collections {
			existing[c.Name] = c.SourcePath
		}
		for i, c := range collections {
			// Unchanged sources need not be online to edit display metadata.
			if existing[c.Name] != c.SourcePath {
				source, err := s.canonicalSource(c.SourcePath)
				if err != nil {
					return d, fmt.Errorf("collection %s: %w", c.Name, err)
				}
				collections[i].SourcePath = source
			}
		}
		if err := validateCollections(collections); err != nil {
			return d, err
		}
		d.Collections = collections
		d.SourcePath = ""
		if len(collections) == 1 && collections[0].Name == "workspace" {
			d.SourcePath = collections[0].SourcePath
		}
	}
	d.Name = strings.TrimSpace(in.Name)
	d.Description = in.Description
	d.UpdatedAt = time.Now().UnixMilli()
	if err := s.saveConfiguration(d); err != nil {
		return d, err
	}
	loaded, err := s.load(id)
	if err != nil {
		return diagnostic(loaded, id, err), nil
	}
	return loaded, nil
}
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return ErrBusy
	}
	dir, configErr := s.directory(id)
	runDir, runErr := s.runtimeDirectory(id, false)
	if configErr != nil && !errors.Is(configErr, ErrNotFound) {
		return configErr
	}
	if runErr != nil && !errors.Is(runErr, ErrNotFound) {
		return runErr
	}
	if errors.Is(configErr, ErrNotFound) && errors.Is(runErr, ErrNotFound) {
		return ErrNotFound
	}
	// Remove the runtime first: interrupted deletion leaves a rebuildable definition.
	if runErr == nil {
		if err := os.RemoveAll(runDir); err != nil {
			return err
		}
	}
	if configErr == nil {
		return os.RemoveAll(dir)
	}
	return nil
}
func (s *Service) Refresh(id string) (Definition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.loadConfiguration(id, false)
	if err != nil {
		return d, err
	}
	if s.busy[id] {
		return d, ErrBusy
	}
	if s.engine == nil {
		return d, fmt.Errorf("KBX engine unavailable")
	}
	dir, err := s.runtimeDirectory(id, true)
	if err != nil {
		return d, err
	}
	// Freeze the desired scope. Completion only writes state, never configuration.
	fingerprint := scopeFingerprint(d.Collections)
	state, err := s.readState(id)
	if err != nil {
		return d, err
	}
	if state.CreatedAt == 0 {
		state.CreatedAt = d.CreatedAt
	}
	state.State, state.Error = "indexing", ""
	state.TaskFingerprint = fingerprint
	state.IndexedAt, state.AppliedFingerprint = 0, ""
	state.UpdatedAt = time.Now().UnixMilli()
	if err = s.saveState(id, state); err != nil {
		return d, err
	}
	d.State, d.Error, d.IndexedAt = "indexing", "", 0
	d.UpdatedAt = state.UpdatedAt
	s.busy[id] = true
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		err := s.engine.Update(ctx, filepath.Join(dir, "index.sqlite"), d.Collections)
		s.mu.Lock()
		defer s.mu.Unlock()
		defer delete(s.busy, id)
		state.UpdatedAt = time.Now().UnixMilli()
		if err != nil {
			state.State, state.Error = "error", err.Error()
		} else {
			state.State, state.IndexedAt, state.AppliedFingerprint = "ready", state.UpdatedAt, fingerprint
		}
		if saveErr := s.saveState(id, state); saveErr != nil {
			log.Printf("[kbases] persist indexing result %s: %v", id, saveErr)
		}
	}()
	return d, nil
}
func (s *Service) Search(ctx context.Context, id string, input SearchInput) (json.RawMessage, error) {
	method := input.Method
	if method == "" {
		method = "query"
	}
	switch method {
	case "query", "search", "vsearch", "gsearch":
	default:
		return nil, fmt.Errorf("method must be query, search, vsearch or gsearch")
	}
	if input.Limit == 0 {
		input.Limit = 10
	}
	return s.Read(ctx, id, method, input.Query, input.Limit, input.Collections...)
}

func (s *Service) Read(ctx context.Context, id, operation, arg string, limit int, selected ...string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.load(id)
	if err != nil {
		return nil, err
	}
	if d.IndexedAt == 0 {
		return nil, fmt.Errorf("build the index before browsing or searching")
	}
	if s.engine == nil {
		return nil, fmt.Errorf("KBX engine unavailable")
	}
	if operation == "search" || operation == "query" || operation == "vsearch" || operation == "gsearch" {
		if strings.TrimSpace(arg) == "" || len(arg) > 8000 {
			return nil, fmt.Errorf("query must contain 1–8000 bytes")
		}
		if limit < 1 || limit > 50 {
			return nil, fmt.Errorf("limit must be between 1 and 50")
		}
	}
	allowed := map[string]bool{}
	names := make([]string, 0, len(d.Collections))
	for _, c := range d.Collections {
		allowed[c.Name] = true
		names = append(names, c.Name)
	}
	if len(selected) > 0 {
		names = nil
		seen := map[string]bool{}
		for _, name := range selected {
			if !allowed[name] {
				return nil, fmt.Errorf("unknown collection: %s", name)
			}
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	if operation == "read" {
		collection, _, valid := DocumentReference(arg)
		if !valid || !allowed[collection] {
			return nil, fmt.Errorf("invalid document reference")
		}
	}
	dir, err := s.runtimeDirectory(id, false)
	if err != nil {
		return nil, err
	}
	db := filepath.Join(dir, "index.sqlite")
	if st, err := os.Lstat(db); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("index is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.engine.Read(ctx, db, operation, arg, limit, names...)
}
