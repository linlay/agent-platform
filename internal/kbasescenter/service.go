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
var idPattern = regexp.MustCompile(`^[a-f0-9]{24}$`)
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
	// SourcePath preserves existing single-source definitions and clients.
	SourcePath string `json:"sourcePath,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	IndexedAt  int64  `json:"indexedAt"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
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
	root   string
	engine Engine
	ctx    context.Context
	mu     sync.RWMutex
	busy   map[string]bool
}

func New(ctx context.Context, root string, engine Engine) (*Service, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("knowledge base center root required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Service{root: root, engine: engine, ctx: ctx, busy: map[string]bool{}}, nil
}
func (s *Service) directory(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", ErrNotFound
	}
	dir := filepath.Join(s.root, id)
	st, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("invalid knowledge base directory")
	}
	return dir, nil
}
func (s *Service) load(id string) (Definition, error) {
	dir, err := s.directory(id)
	if err != nil {
		return Definition{}, err
	}
	p := filepath.Join(dir, "library.json")
	st, err := os.Lstat(p)
	if err != nil {
		return Definition{}, err
	}
	if !st.Mode().IsRegular() {
		return Definition{}, fmt.Errorf("invalid library definition")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Definition{}, err
	}
	var d Definition
	if err = json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	if d.ID != id {
		return d, fmt.Errorf("library identity mismatch")
	}
	if len(d.Collections) == 0 && d.SourcePath != "" {
		d.Collections = []Collection{{Name: "workspace", SourcePath: d.SourcePath}}
	}
	if err := validateCollections(d.Collections); err != nil {
		return d, err
	}
	if d.State == "indexing" && !s.busy[id] {
		d.State = "error"
		d.Error = "Indexing was interrupted; retry updating the index"
	}
	return d, nil
}
func (s *Service) save(d Definition) error {
	dir, err := s.directory(d.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".library-")
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
	return os.Rename(f.Name(), filepath.Join(dir, "library.json"))
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
	// Never scan the center itself, including when an ancestor is selected.
	rel, _ := filepath.Rel(source, s.root)
	back, _ := filepath.Rel(s.root, source)
	inside := func(p string) bool { return p != ".." && !strings.HasPrefix(p, ".."+string(filepath.Separator)) }
	if inside(rel) || inside(back) {
		return "", fmt.Errorf("source directory must not overlap the knowledge base center")
	}
	return source, nil
}
func (s *Service) List() ([]Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := []Definition{}
	for _, entry := range entries {
		if !idPattern.MatchString(entry.Name()) {
			continue
		}
		d, err := s.load(entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}
func (s *Service) Get(id string) (Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
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
	dir := filepath.Join(s.root, d.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return Definition{}, err
	}
	if err := s.save(d); err != nil {
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
	d, err := s.load(id)
	if err != nil {
		return d, err
	}
	if s.busy[id] {
		return d, ErrBusy
	}
	if in.SourcePath != "" && in.SourcePath != d.SourcePath {
		return d, fmt.Errorf("sourcePath is immutable; create a new knowledge base to change its source")
	}
	if in.Collections != nil {
		if len(in.Collections) != len(d.Collections) {
			return d, fmt.Errorf("collections are immutable; create a new knowledge base to change its sources")
		}
		existing := map[string]string{}
		for _, c := range d.Collections {
			existing[c.Name] = c.SourcePath
		}
		seen := map[string]bool{}
		for _, c := range in.Collections {
			if existing[c.Name] != c.SourcePath || seen[c.Name] {
				return d, fmt.Errorf("collections are immutable; create a new knowledge base to change its sources")
			}
			seen[c.Name] = true
		}
	}
	d.Name = strings.TrimSpace(in.Name)
	d.Description = in.Description
	d.UpdatedAt = time.Now().UnixMilli()
	return d, s.save(d)
}
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(id); err != nil {
		return err
	}
	if s.busy[id] {
		return ErrBusy
	}
	dir, err := s.directory(id)
	if err != nil {
		return err
	}
	// Rename first so a partial filesystem deletion cannot leave a visible half-library.
	trash := filepath.Join(s.root, ".deleted-"+id)
	if err = os.Rename(dir, trash); err != nil {
		return err
	}
	return os.RemoveAll(trash)
}
func (s *Service) Refresh(id string) (Definition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load(id)
	if err != nil {
		return d, err
	}
	if s.busy[id] {
		return d, ErrBusy
	}
	if s.engine == nil {
		return d, fmt.Errorf("KBX engine unavailable")
	}
	d.State = "indexing"
	d.Error = ""
	d.UpdatedAt = time.Now().UnixMilli()
	if err = s.save(d); err != nil {
		return d, err
	}
	s.busy[id] = true
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		err := s.engine.Update(ctx, filepath.Join(s.root, id, "index.sqlite"), d.Collections)
		s.mu.Lock()
		defer s.mu.Unlock()
		defer delete(s.busy, id)
		d.UpdatedAt = time.Now().UnixMilli()
		if err != nil {
			d.State = "error"
			d.Error = err.Error()
		} else {
			d.State = "ready"
			d.IndexedAt = d.UpdatedAt
		}
		if saveErr := s.save(d); saveErr != nil {
			log.Printf("[kbases-center] persist indexing result %s: %v", id, saveErr)
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
	db := filepath.Join(s.root, id, "index.sqlite")
	if st, err := os.Lstat(db); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("index is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.engine.Read(ctx, db, operation, arg, limit, names...)
}
