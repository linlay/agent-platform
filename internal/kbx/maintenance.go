package kbx

import (
	"agent-platform/internal/knowledge"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type indexState struct {
	Documents int `json:"documents"`
	FullText  struct {
		Ready bool   `json:"ready"`
		State string `json:"state"`
	} `json:"fullText"`
	Vector struct {
		Complete   bool   `json:"complete"`
		State      string `json:"state"`
		Pending    int    `json:"pendingContentUnits"`
		Compatible *bool  `json:"configuredContractCompatible"`
	} `json:"vector"`
}
type maintenanceResponse struct {
	SchemaVersion int             `json:"schemaVersion"`
	Type          string          `json:"type"`
	Operation     string          `json:"operation"`
	Status        string          `json:"status"`
	ExitCode      *int            `json:"exitCode"`
	StopReason    string          `json:"stopReason"`
	Data          json.RawMessage `json:"data"`
	Run           *struct {
		AttemptedFiles int                                                       `json:"attemptedFiles"`
		Committed      struct{ Added, Modified, Deleted, Unchanged, Chunks int } `json:"committed"`
		FailedFiles    int                                                       `json:"failedFiles"`
		ScanErrors     int                                                       `json:"scanErrors"`
	} `json:"run"`
	Index *struct {
		Selected indexState `json:"selected"`
	} `json:"index"`
	Continuation struct {
		CanRetry bool `json:"canRetry"`
		Pending  bool `json:"pending"`
	} `json:"continuation"`
	Failures struct {
		Items     []json.RawMessage `json:"items"`
		Total     int               `json:"total"`
		Truncated bool              `json:"truncated"`
	} `json:"failures"`
	Error *struct{ Code, Message string } `json:"error"`
}

func (r maintenanceResponse) failure() error {
	if r.Error != nil {
		return fmt.Errorf("KBX %s %s: %s", r.Operation, r.Error.Code, r.Error.Message)
	}
	return fmt.Errorf("KBX %s %s (%s; %d file failures)", r.Operation, r.Status, r.StopReason, r.Failures.Total)
}
func (m *Manager) maintenance(ctx context.Context, l library, cfg []byte, operation string, args ...string) (maintenanceResponse, error) {
	var r maintenanceResponse
	data, runErr := m.runner.Run(ctx, l.database, cfg, append(args, "--format", "json")...)
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("KBX %s returned no valid maintenance response (completion unknown)", operation)
	}
	if r.SchemaVersion != 1 || r.Type != "kbx.maintenance.response" || r.Operation != operation || r.ExitCode == nil {
		return r, fmt.Errorf("unsupported KBX maintenance protocol for %s", operation)
	}
	if runErr == nil && *r.ExitCode != 0 {
		return r, fmt.Errorf("KBX process/response exit code mismatch")
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && exit.ExitCode() != *r.ExitCode {
			return r, fmt.Errorf("KBX process/response exit code mismatch")
		}
		if *r.ExitCode == 0 {
			return r, fmt.Errorf("KBX process failed despite success response")
		}
	}
	if r.Status != "complete" || *r.ExitCode != 0 || r.Error != nil || r.Failures.Total > 0 || (r.Run != nil && (r.Run.FailedFiles > 0 || r.Run.ScanErrors > 0)) {
		return r, r.failure()
	}
	return r, nil
}
func (m *Manager) probeMaintenance(ctx context.Context) error {
	b, err := m.runner.Run(ctx, "unused", []byte("{}"), "capabilities", "--format", "json")
	if err != nil {
		return fmt.Errorf("KBX maintenance capabilities unavailable: %w", err)
	}
	var c struct {
		SchemaVersion int
		Type          string
		Maintenance   struct {
			SchemaVersion                          int
			JSONCommands                           []string
			StructuredErrors, RegisterWithoutIndex bool
			Includes                               struct {
				Expression, Multiple            string
				CaseSensitive, LiteralSeparator bool
			}
			Ignore      struct{ Multiple, CaseSensitive, BuiltinsOverrideIncludes bool }
			PathUpdates struct {
				SchemaVersion                                             int
				SingleCollection, FilesOnly, Symlinks, RunsUpdateCommands bool
			}
		}
	}
	if json.Unmarshal(b, &c) != nil || c.SchemaVersion != 1 || c.Type != "kbx.capabilities" || c.Maintenance.SchemaVersion != 1 || !c.Maintenance.StructuredErrors || !c.Maintenance.RegisterWithoutIndex || c.Maintenance.PathUpdates.SchemaVersion != 1 || !c.Maintenance.PathUpdates.SingleCollection || !c.Maintenance.PathUpdates.FilesOnly || c.Maintenance.PathUpdates.Symlinks || c.Maintenance.PathUpdates.RunsUpdateCommands || c.Maintenance.Includes.Expression != "globset" || c.Maintenance.Includes.Multiple != "braceAlternation" || !c.Maintenance.Includes.CaseSensitive || !c.Maintenance.Ignore.Multiple || !c.Maintenance.Ignore.CaseSensitive || !c.Maintenance.Ignore.BuiltinsOverrideIncludes {
		return fmt.Errorf("KBX maintenance protocol v1 with safe path updates is required; synchronize the managed builtin")
	}
	for _, want := range []string{"collection.add", "collection.list", "collection.show", "status", "update", "embed"} {
		found := false
		for _, got := range c.Maintenance.JSONCommands {
			found = found || got == want
		}
		if !found {
			return fmt.Errorf("KBX JSON command %s is unavailable", want)
		}
	}
	return nil
}

// Only pass globs with identical slash semantics in Platform and KBX. Other
// rules fail closed until KBX exposes literalSeparator=true for source selection.
func maintenancePattern(pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("empty KBX source glob")
	}
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if p == "**" {
			continue
		}
		if strings.ContainsAny(p, "*?") {
			// A suffix-only filename after **/ is equivalent even if * crosses '/'.
			if i != len(parts)-1 || i == 0 || parts[i-1] != "**" || !strings.HasPrefix(p, "*") || strings.ContainsAny(p[1:], "*?") {
				return "", fmt.Errorf("KBX source glob %q has incompatible directory semantics; use explicit paths, dir/** or **/*.ext", pattern)
			}
		}
	}
	return strings.ReplaceAll(pattern, ",", "[,]"), nil
}

func (m *Manager) performRefresh(ctx context.Context, w *collectionUpdate, j *updatePaths) error {
	l := w.library
	name := l.collection
	if name == "" {
		name = "workspace"
	}
	current, rootErr := filepath.EvalSymlinks(l.spec.WorkspaceRoot)
	if rootErr != nil || current != l.spec.WorkspaceRoot {
		return fmt.Errorf("KBX source root is missing or its canonical identity changed")
	}
	if err := m.probeMaintenance(ctx); err != nil {
		return err
	}
	patterns := []string{}
	for _, p := range l.source.Include {
		v, e := maintenancePattern(p)
		if e != nil {
			return e
		}
		patterns = append(patterns, v)
	}
	pattern := "**/*"
	if len(patterns) == 1 {
		pattern = patterns[0]
	} else if len(patterns) > 1 {
		pattern = "{" + strings.Join(patterns, ",") + "}"
	}
	ignores := []string{".kbx-platform/**"}
	for _, p := range l.source.Exclude {
		v, e := maintenancePattern(p)
		if e != nil {
			return e
		}
		ignores = append(ignores, v)
	}
	// Runtime/state directories may be deliberately placed inside the source root.
	for _, root := range []string{m.options.RuntimeDir, m.options.StateDir} {
		if root == "" {
			continue
		}
		rel, e := filepath.Rel(l.spec.WorkspaceRoot, root)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return fmt.Errorf("KBX runtime/state directory must not equal source root")
			}
			ignores = append(ignores, literalGlob(filepath.ToSlash(rel))+"/**")
		}
	}
	cfg, err := m.config(l, true)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(l.database), 0700); err != nil {
		return err
	}
	var r maintenanceResponse
	type collection struct {
		Name, Path, Pattern string
		Ignore              []string
		Chunking            struct {
			Strategy     string
			MaxChars     int `json:"max_chars"`
			OverlapChars int `json:"overlap_chars"`
		}
	}
	var catalog struct{ Collections []collection }
	if _, statErr := os.Stat(l.database); statErr == nil {
		r, err = m.retryMaintenance(ctx, l, cfg, "collection.list", "collection", "list")
		if err != nil {
			return err
		}
		if err = json.Unmarshal(r.Data, &catalog); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	registered := false
	for _, c := range catalog.Collections {
		if c.Name == name {
			if filepath.Clean(c.Path) != l.spec.WorkspaceRoot {
				return fmt.Errorf("KBX source identity does not match collection")
			}
			registered = true
		}
	}
	if !registered {
		if _, err = m.retryMaintenance(ctx, l, cfg, "collection.add", "collection", "add", l.spec.WorkspaceRoot, "--name", name, "--no-index", "--pattern", pattern); err != nil {
			return err
		}
	}
	// These setters have no JSON output contract. Only their exit status is used;
	// query and compare the resulting configuration before reading any source.
	commands := [][]string{{"collection", "set-pattern", name, pattern}, append([]string{"collection", "set-ignore", name}, ignores...)}
	defaultCommand := "include"
	if l.source.DefaultQuery != nil && !*l.source.DefaultQuery {
		defaultCommand = "exclude"
	}
	commands = append(commands, []string{"collection", defaultCommand, name})
	chunk, err := knowledge.ResolveSourceChunk(knowledge.ChunkSettings{}, l.source.Chunk)
	if err != nil {
		return err
	}
	maxChars, overlap := chunk.MaxChars, chunk.OverlapChars
	commands = append(commands, []string{"collection", "set-chunking", name, "--chunk-strategy", chunk.Strategy, "--max-chars", strconv.Itoa(maxChars), "--overlap-chars", strconv.Itoa(overlap)})
	for _, args := range commands {
		if _, err = m.runner.Run(ctx, l.database, cfg, args...); err != nil {
			return err
		}
	}
	r, err = m.retryMaintenance(ctx, l, cfg, "collection.show", "collection", "show", name)
	if err != nil {
		return err
	}
	var actual collection
	if json.Unmarshal(r.Data, &actual) != nil || actual.Name != name || filepath.Clean(actual.Path) != l.spec.WorkspaceRoot || actual.Pattern != pattern || !slices.Equal(actual.Ignore, ignores) || actual.Chunking.Strategy != chunk.Strategy || actual.Chunking.MaxChars != maxChars || actual.Chunking.OverlapChars != overlap {
		return fmt.Errorf("KBX source selection configuration was not applied")
	}
	args := []string{"update", "-c", name, "--no-commands"}
	if j.incremental {
		dir, err := os.MkdirTemp("", "platform-kbx-paths-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "changes.json")
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "paths": j.paths})
		if err = os.WriteFile(file, b, 0600); err != nil {
			return err
		}
		args = append(args, "--paths-from", file)
	}
	r, err = m.retryMaintenance(ctx, l, cfg, "update", args...)
	if r.Index != nil {
		s := r.Index.Selected
		w.index = &s
	}
	if err == nil {
		w.initialized = true
	}
	if err != nil {
		return err
	}
	if r.Index == nil || !r.Index.Selected.FullText.Ready {
		return fmt.Errorf("KBX update did not confirm readable full-text index")
	}
	if m.skipEmbedding {
		return nil
	}
	var models struct {
		Models struct{ Embedding json.RawMessage }
	}
	if json.Unmarshal(cfg, &models) != nil {
		return fmt.Errorf("invalid KBX model snapshot")
	}
	embedding := len(models.Models.Embedding) > 0 && string(models.Models.Embedding) != "null"
	if embedding && r.Index.Selected.Documents > 0 {
		args = []string{"embed", "-c", name, "--timeout", "30"}
		r, err = m.retryMaintenance(ctx, l, cfg, "embed", args...)
		if r.Index != nil {
			s := r.Index.Selected
			w.index = &s
		}
		if err != nil {
			return err
		}
		if r.Index == nil || !r.Index.Selected.Vector.Complete || (r.Index.Selected.Vector.Compatible != nil && !*r.Index.Selected.Vector.Compatible) {
			return fmt.Errorf("KBX embedding completed without compatible, complete vectors")
		}
	}
	return nil
}
func literalGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '{', '}', ',':
			b.WriteRune('[')
			b.WriteRune(r)
			b.WriteRune(']')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
func (m *Manager) retryMaintenance(ctx context.Context, l library, cfg []byte, op string, args ...string) (maintenanceResponse, error) {
	var r maintenanceResponse
	var err error
	embedded := 0
	for attempt := 0; attempt < 3; attempt++ {
		r, err = m.maintenance(ctx, l, cfg, op, args...)
		if r.Run != nil {
			embedded += r.Run.Committed.Chunks
			r.Run.Committed.Chunks = embedded
		}
		retry := r.Error != nil && r.Error.Code == "INDEX_BUSY"
		retry = retry || (op == "embed" && !slices.Contains(args, "--force") && r.Status == "partial" && r.StopReason == "SESSION_LIMIT" && r.Continuation.CanRetry)
		if err == nil || !retry || attempt == 2 {
			return r, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return r, ctx.Err()
		case <-timer.C:
		}
	}
	return r, err
}
