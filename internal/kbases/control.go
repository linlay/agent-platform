package kbases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
)

// ControlPlan contains only definition data. Worker state is not an approval baseline.
type ControlPlan struct {
	Action             string       `json:"action"`
	Digest             string       `json:"digest"`
	RequestDigest      string       `json:"requestDigest"`
	Revision           string       `json:"baseRevision,omitempty"`
	Before             *Definition  `json:"before,omitempty"`
	After              *Definition  `json:"after,omitempty"`
	Reused             bool         `json:"reused"`
	Completed          bool         `json:"completed,omitempty"`
	Candidate          Input        `json:"candidate"`
	SourcePermissions  []Collection `json:"sourcePermissions"`
	SourceGrantChanged bool         `json:"sourceGrantChanged"`
}

var ErrRevisionConflict = errors.New("revision_conflict")
var ErrApprovalStale = errors.New("approval_stale")

func managementFailure(err error, started bool) error {
	if err != nil && !started {
		return &contracts.MutationError{State: "not_started", Err: err}
	}
	return err
}

func (s *Service) checkManagementReady(id string) error {
	lock := s.libraryLock(id)
	if !lock.TryLock() {
		return ErrBusy
	}
	defer lock.Unlock()
	if s.isBusy(id) {
		return ErrBusy
	}
	return nil
}

func sourceGrantDigest(collections []Collection) string {
	type grant struct {
		SourcePath       string
		Include, Exclude []string
		Editable         bool
	}
	grants := make([]grant, 0, len(collections))
	for _, c := range collections {
		g := grant{SourcePath: c.SourcePath, Include: append([]string(nil), c.Include...), Exclude: append([]string(nil), c.Exclude...), Editable: c.Editable}
		sort.Strings(g.Include)
		sort.Strings(g.Exclude)
		grants = append(grants, g)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].SourcePath < grants[j].SourcePath })
	return controlDigest(grants)
}

type controlReceipt struct {
	Plan   ControlPlan    `json:"plan"`
	State  string         `json:"state"`
	Result map[string]any `json:"result,omitempty"`
}

func controlDigest(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func decodeControl(value any, out any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("knowledge library request exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func definitionConfiguration(d Definition) configuration {
	return configuration{Name: d.Name, Description: d.Description, Collections: d.Collections,
		Retrieval: d.Retrieval, Chunk: d.Chunk, TextEncoding: d.TextEncoding, Models: d.Models}
}

func (s *Service) configurationRevision(id string) (string, error) {
	dir, err := s.directory(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if _, runErr := s.runtimeDirectory(id, false); runErr == nil {
				// A stable missing-definition baseline permits orphan cleanup, but
				// cannot authorize deleting a definition subsequently recreated.
				return controlDigest([]string{"orphaned-library", id}), nil
			}
		}
		return "", err
	}
	b, _, err := readRegular(filepath.Join(dir, "library.yml"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) checkRevision(id, expected string) error {
	actual, err := s.configurationRevision(id)
	if err != nil {
		return err
	}
	if expected == "" || expected != actual {
		return fmt.Errorf("%w: reread kbase_library_query.get and use its baseRevision", ErrRevisionConflict)
	}
	return nil
}

// ValidateBinding checks the persisted definition, not index readiness or diagnostic Get results.
func (s *Service) ValidateBinding(id string) error {
	_, err := s.BindingDefinition(id)
	return err
}

// BindingDefinition returns a valid configuration and its canonical effective
// sources from one read lease, independent of index readiness or diagnostics.
func (s *Service) BindingDefinition(id string) (Definition, error) {
	lock := s.libraryLock(id)
	lock.RLock()
	defer lock.RUnlock()
	d, err := s.loadConfiguration(id, true)
	if err != nil {
		return Definition{}, err
	}
	return s.effectiveDefinition(d), nil
}

func (s *Service) normalizeCandidate(in Input) (Input, Definition, error) {
	if err := validate(in); err != nil {
		return in, Definition{}, err
	}
	if in.SourcePath != "" && in.Collections != nil {
		return in, Definition{}, fmt.Errorf("sourcePath and collections are mutually exclusive")
	}
	collections := append([]Collection(nil), in.Collections...)
	if in.SourcePath != "" {
		collections = []Collection{{Name: "workspace", SourcePath: in.SourcePath}}
	}
	if err := validateCollections(collections); err != nil {
		return in, Definition{}, err
	}
	for i := range collections {
		source, err := s.canonicalSource(collections[i].SourcePath)
		if err != nil {
			return in, Definition{}, fmt.Errorf("collection %s: %w", collections[i].Name, err)
		}
		collections[i].SourcePath = source
	}
	if err := validateCollections(collections); err != nil {
		return in, Definition{}, err
	}
	in.Name, in.SourcePath, in.Collections = strings.TrimSpace(in.Name), "", collections
	d := Definition{Name: in.Name, Description: in.Description, Collections: collections}
	if err := applyLibrarySettings(&d, in); err != nil {
		return in, d, err
	}
	// Normalized settings and effective scopes are the exact candidate reviewed by the user.
	in.TextEncoding = &d.TextEncoding
	return in, d, nil
}

func (s *Service) equivalentConfiguration(d Definition) string {
	c := definitionConfiguration(s.effectiveDefinition(d))
	c.Name, c.Description = "", ""
	sort.Slice(c.Collections, func(i, j int) bool { return c.Collections[i].Name < c.Collections[j].Name })
	for i := range c.Collections {
		c.Collections[i].Include = append([]string(nil), c.Collections[i].Include...)
		c.Collections[i].Exclude = append([]string(nil), c.Collections[i].Exclude...)
		sort.Strings(c.Collections[i].Include)
		sort.Strings(c.Collections[i].Exclude)
	}
	return controlDigest(c)
}

// mergeControlPatch preserves omission, replaces arrays, and removes null fields.
// The resulting full definition is validated before publication.
func mergeControlPatch(base, patch map[string]any) error {
	for k, v := range patch {
		if v == nil {
			delete(base, k)
			continue
		}
		if object, ok := v.(map[string]any); ok {
			old, _ := base[k].(map[string]any)
			if old == nil {
				old = map[string]any{}
			}
			if err := mergeControlPatch(old, object); err != nil {
				return err
			}
			base[k] = old
		} else {
			base[k] = v
		}
	}
	return nil
}

func (s *Service) receiptPath(key string) (string, error) {
	if key == "" || s.options.StateDir == "" {
		return "", fmt.Errorf("knowledge library invocation identity and protected state directory are required")
	}
	return filepath.Join(s.options.StateDir, "kbases-control", controlDigest(key)+".json"), nil
}

func readReceipt(path string) (*controlReceipt, error) {
	b, _, err := readRegular(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt controlReceipt
	if err := json.Unmarshal(b, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (s *Service) PrepareControl(action string, args map[string]any, key string) (*ControlPlan, error) {
	s.management.Lock()
	defer s.management.Unlock()
	return s.prepareControl(action, args, key)
}

func (s *Service) prepareControl(action string, args map[string]any, key string) (*ControlPlan, error) {
	requestDigest := controlDigest([]any{action, args})
	if key != "" {
		path, err := s.receiptPath(key)
		if err != nil {
			return nil, err
		}
		r, err := readReceipt(path)
		if err != nil {
			return nil, err
		}
		if r != nil {
			if r.Plan.RequestDigest != requestDigest {
				return nil, fmt.Errorf("invocation_conflict: this request identity already used different arguments")
			}
			if r.State != "completed" {
				return nil, &contracts.MutationError{State: "unknown", Err: fmt.Errorf("library mutation outcome unknown; inspect libraries before a new request")}
			}
			p := r.Plan
			p.Completed = true
			return &p, nil
		}
	}
	p := &ControlPlan{Action: action, RequestDigest: requestDigest}
	var in Input
	if action == "create" {
		if err := decodeControl(args["definition"], &in); err != nil {
			return nil, err
		}
		if in.BaseRevision != "" {
			return nil, fmt.Errorf("create definition must not contain baseRevision")
		}
	} else if action == "update" || action == "delete" {
		id, _ := args["libraryId"].(string)
		revision, _ := args["baseRevision"].(string)
		if err := s.checkManagementReady(id); err != nil {
			return nil, err
		}
		if err := s.checkRevision(id, revision); err != nil {
			return nil, err
		}
		before, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		p.Before, p.Revision = &before, before.BaseRevision
		if action == "delete" {
			if s.options.References != nil {
				if refs := s.options.References(id); len(refs) > 0 {
					return nil, &ReferencedError{Agents: refs}
				}
			}
			p.SourcePermissions = s.effectiveDefinition(before).Collections
			p.Digest = controlDigest([]any{action, requestDigest, p.Revision, definitionConfiguration(before), id, p.SourcePermissions})
			return p, nil
		}
		patch, ok := args["patch"].(map[string]any)
		if !ok || len(patch) == 0 {
			return nil, fmt.Errorf("update requires a nonempty patch object")
		}
		if _, exists := patch["baseRevision"]; exists {
			return nil, fmt.Errorf("baseRevision belongs outside patch")
		}
		if err := decodeControl(patch, &Input{}); err != nil {
			return nil, err
		}
		if before.Orphaned {
			return nil, fmt.Errorf("orphaned library has no definition; delete it or create a new library")
		}
		beforeForMerge := before
		if _, err := s.loadConfiguration(id, true); err != nil {
			_, hasName := patch["name"]
			_, hasCollections := patch["collections"]
			_, hasSource := patch["sourcePath"]
			if !hasName || !hasCollections && !hasSource {
				return nil, fmt.Errorf("invalid definition requires an explicit replacement name and collections/sourcePath: %w", err)
			}
			// A malformed definition has no trustworthy merge baseline.
			beforeForMerge = Definition{ID: id, BaseRevision: revision}
		}
		b, _ := json.Marshal(definitionConfiguration(beforeForMerge))
		var base map[string]any
		_ = json.Unmarshal(b, &base)
		if _, source := patch["sourcePath"]; source {
			if _, collections := patch["collections"]; collections {
				return nil, fmt.Errorf("sourcePath and collections are mutually exclusive")
			}
			delete(base, "collections")
		}
		if err := mergeControlPatch(base, patch); err != nil {
			return nil, err
		}
		if err := decodeControl(base, &in); err != nil {
			return nil, err
		}
		in.BaseRevision = revision
	} else {
		return nil, fmt.Errorf("unsupported knowledge library action")
	}
	var err error
	var after Definition
	p.Candidate, after, err = s.normalizeCandidate(in)
	if err != nil {
		return nil, err
	}
	p.After = &after
	if p.Before != nil {
		p.After.ID = p.Before.ID
	}
	if action == "create" {
		libraries, err := s.List()
		if err != nil {
			return nil, err
		}
		for _, library := range libraries {
			if library.Orphaned || library.InvalidID || s.ValidateBinding(library.ID) != nil {
				continue
			}
			if s.equivalentConfiguration(library) == s.equivalentConfiguration(after) {
				p.Reused, p.Revision, p.After = true, library.BaseRevision, &library
				break
			}
		}
	}
	p.SourcePermissions = s.effectiveDefinition(*p.After).Collections
	p.SourceGrantChanged = action == "create" && !p.Reused
	if p.Before != nil {
		p.SourceGrantChanged = sourceGrantDigest(s.effectiveDefinition(*p.Before).Collections) != sourceGrantDigest(p.SourcePermissions)
	}
	p.Digest = controlDigest([]any{action, requestDigest, p.Revision, definitionConfiguration(*p.After), p.After.ID, p.Reused, p.SourcePermissions, p.SourceGrantChanged})
	return p, nil
}

// ExecuteControl rechecks the candidate under the same gate as HTTP mutations.
// A durable intent is never replayed after an interrupted outcome.
func (s *Service) ExecuteControl(action string, args map[string]any, key string, authorize func(string) bool) (map[string]any, error) {
	s.management.Lock()
	defer s.management.Unlock()
	p, err := s.prepareControl(action, args, key)
	if err != nil {
		return nil, err
	}
	path, err := s.receiptPath(key)
	if err != nil {
		return nil, err
	}
	if p.Completed {
		r, err := readReceipt(path)
		if err != nil {
			return nil, err
		}
		return r.Result, nil
	}
	if !p.Reused && (authorize == nil || !authorize(p.Digest)) {
		return nil, fmt.Errorf("approval_required: approve this exact library definition and baseline")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	r := controlReceipt{Plan: *p, State: "started"}
	b, _ := json.Marshal(r)
	if err := atomicWrite(dir, filepath.Base(path), b); err != nil {
		return nil, err
	}
	var d Definition
	if p.Reused {
		d, err = s.Get(p.After.ID)
	} else {
		switch action {
		case "create":
			d, err = s.create(p.Candidate, false, p.SourcePermissions)
		case "update":
			d, err = s.edit(p.Before.ID, p.Candidate, p.SourcePermissions, true)
		case "delete":
			err = s.delete(p.Before.ID, p.Revision)
		}
	}
	if err != nil {
		var failure *contracts.MutationError
		if (errors.As(err, &failure) && failure.State == "not_started") || p.Reused {
			if cleanupErr := os.Remove(path); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				return nil, &contracts.MutationError{State: "not_started", Err: errors.Join(err, fmt.Errorf("receipt cleanup failed: %w", cleanupErr))}
			}
			return nil, err
		}
		return nil, &contracts.MutationError{State: "unknown", Err: err}
	}
	result := map[string]any{"libraryId": d.ID, "created": action == "create" && !p.Reused, "reused": p.Reused}
	if action == "delete" {
		result["libraryId"], result["deleted"] = p.Before.ID, true
	} else {
		result["library"], result["baseRevision"] = d, d.BaseRevision
	}
	r.State, r.Result = "completed", result
	b, _ = json.Marshal(r)
	if err := atomicWrite(dir, filepath.Base(path), b); err != nil {
		return nil, &contracts.MutationError{State: "unknown", Err: err}
	}
	return result, nil
}

// LibraryStatus is available before KBX has produced any index.
func (s *Service) LibraryStatus(ctx context.Context, id string) (map[string]any, error) {
	d, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision, "state": d.State,
		"error": d.Error, "refreshError": d.RefreshError, "indexedAt": d.IndexedAt,
		"indexing": d.Indexing, "stale": d.Stale, "degraded": d.Degraded,
		"vectorsPending": d.VectorsPending, "orphaned": d.Orphaned, "statsAvailable": false}
	if d.IndexedAt > 0 {
		stats, err := s.Read(ctx, id, "status", "", 0)
		if err != nil {
			result["statsError"] = err.Error()
		} else {
			result["statistics"], result["statsAvailable"] = stats, true
		}
	}
	return result, nil
}

// IsRevisionConflict is shared by HTTP and management tool error mapping.
func IsRevisionConflict(err error) bool {
	return errors.Is(err, ErrRevisionConflict)
}
