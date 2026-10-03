package adminsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/pathutil"
)

const maxControlContent = 1 << 20

type ControlTarget struct {
	ResourceType string `json:"resourceType"`
	ResourceKey  string `json:"resourceKey"`
	Path         string `json:"path,omitempty"`
}
type ControlChange struct {
	ControlTarget
	Action        string   `json:"action"`
	Content       string   `json:"content,omitempty"`
	BaseRevision  string   `json:"baseRevision,omitempty"`
	PreservePaths []string `json:"preservePaths,omitempty"`
	MCPURL        string   `json:"mcpUrl,omitempty"`
}
type ControlSource struct {
	ControlTarget
	Content       string   `json:"content"`
	BaseRevision  string   `json:"baseRevision"`
	RedactedPaths []string `json:"redactedPaths"`
	Editable      bool     `json:"editable"`
	Reason        string   `json:"reason,omitempty"`
}
type ControlPlan struct {
	Change              ControlChange
	Before              string
	After               string
	GeneratedFiles      map[string]string
	Digest              string
	root, rel, revision string
	exists              bool
}
type ControlService struct {
	Reload     func(context.Context, string) error
	Coordinate func(context.Context, string, func(context.Context) error) error
	Mutations  *Service
	Config     config.Config
	Registry   catalog.Registry
	Models     *models.ModelRegistry
}

func controlHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func safePart(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, "/\\\x00:")
}
func noLinks(path string) error {
	path = filepath.Clean(path)
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not editable")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
func (s *ControlService) resolve(t ControlTarget) (root, rel string, err error) {
	parts := strings.Split(t.ResourceKey, "/")
	if len(parts) > 2 || len(parts) == 2 && t.ResourceType != "skill" {
		return "", "", fmt.Errorf("invalid resourceKey")
	}
	for _, part := range parts {
		if !safePart(part) {
			return "", "", fmt.Errorf("invalid resourceKey")
		}
	}
	var base, primary string
	switch t.ResourceType {
	case "agent":
		base = s.Config.Paths.AgentsDir
		primary = "agent.yml"
	case "team":
		base = s.Config.Paths.TeamsDir
		primary = "team.yml"
	case "skill":
		base = s.Config.Paths.SkillsCenterDir
		primary = "SKILL.md"
	case "connector":
		base = s.Config.Paths.EffectiveConnectorsCenterDir()
		primary = "connector.json"
	default:
		return "", "", fmt.Errorf("resourceType is not editable")
	}
	if base == "" {
		return "", "", fmt.Errorf("source root is not configured")
	}
	canonical, e := pathutil.Canonicalize(base)
	if e != nil {
		return "", "", e
	}
	base = canonical.Host
	root = filepath.Join(base, parts[0])
	if t.ResourceType == "skill" && len(parts) == 1 {
		if _, statErr := os.Lstat(filepath.Join(root, "package.json")); statErr == nil {
			return "", "", fmt.Errorf("skill package requires resourceKey package/member; whole-package mutation is not supported")
		} else if !os.IsNotExist(statErr) {
			return "", "", statErr
		}
	}
	rel = t.Path
	if rel == "" {
		rel = primary
		if t.ResourceType == "team" {
			if _, e := os.Lstat(filepath.Join(root, "team.yaml")); e == nil {
				rel = "team.yaml"
			}
		}
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, "\\") || strings.Contains(rel, "\x00") {
		return "", "", fmt.Errorf("invalid path")
	}
	for _, p := range strings.Split(rel, "/") {
		if !safePart(p) {
			return "", "", fmt.Errorf("invalid path")
		}
	}
	switch t.ResourceType {
	case "agent":
		if rel != "agent.yml" && rel != "SOUL.md" && rel != "AGENTS.md" {
			return "", "", fmt.Errorf("unsupported agent path")
		}
	case "team":
		if rel != "team.yml" && rel != "team.yaml" {
			return "", "", fmt.Errorf("unsupported team path")
		}
	case "connector":
		if rel != "connector.json" {
			return "", "", fmt.Errorf("only connector.json is editable")
		}
	}
	if len(parts) == 2 {
		rel = filepath.Join(parts[1], rel)
	}
	if err = noLinks(filepath.Join(root, rel)); err != nil {
		return "", "", err
	}
	return root, rel, nil
}
func treeRevision(root string) (string, bool, error) {
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	h := sha256.New()
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("resource contains a symlink")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("resource contains non-regular files")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		total += info.Size()
		if total > 64<<20 {
			return fmt.Errorf("resource exceeds management size limit")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.Mode().Perm(), len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), true, err
}
func parseDefinition(content string) (map[string]any, error) {
	v, e := config.LoadYAMLTreeBytes([]byte(content))
	if e != nil {
		return nil, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("definition must be a mapping")
	}
	return m, nil
}
func node(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
func stringList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, a := range x {
			if z, ok := a.(string); ok {
				out = append(out, z)
			}
		}
	case []string:
		out = x
	}
	return out
}
func (s *ControlService) Read(t ControlTarget) (ControlSource, error) {
	out := ControlSource{ControlTarget: t, RedactedPaths: []string{}, Editable: !connector.IsBuiltin(t.ResourceKey)}
	root, rel, e := s.resolve(t)
	if t.ResourceType == "connector" && connector.IsBuiltin(t.ResourceKey) {
		pkg, err := s.Config.Paths.ConnectorSources().Load(t.ResourceKey)
		if err != nil {
			return out, err
		}
		root, rel, e = pkg.Dir, "connector.json", nil
		out.Reason = "built-in resources are read-only"
	}
	if e != nil {
		return out, e
	}
	revision, exists := "", true
	if out.Editable {
		revision, exists, e = treeRevision(root)
	}
	if e != nil {
		return out, e
	}
	if !exists {
		return out, os.ErrNotExist
	}
	b, e := os.ReadFile(filepath.Join(root, rel))
	if e != nil {
		return out, e
	}
	if len(b) > maxControlContent || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return out, fmt.Errorf("file is not bounded UTF-8 text")
	}
	out.Content = string(b)
	if !out.Editable {
		revision = controlHash(b)
	}
	out.BaseRevision = revision
	out.Path = t.Path
	if out.Path == "" {
		out.Path = filepath.Base(rel)
	}
	if t.ResourceType == "agent" && filepath.Base(rel) == "agent.yml" {
		out.Content, out.RedactedPaths, e = redactAgentEnvironment(string(b))
		if e != nil {
			return ControlSource{}, e
		}
	}
	if t.ResourceType == "connector" {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return ControlSource{}, fmt.Errorf("invalid connector JSON; cannot safely redact")
		} else {
			redactControlSecrets(m, "", &out.RedactedPaths)
			if len(out.RedactedPaths) > 0 {
				out.Editable = false
				out.Reason = "inline credentials must be managed through connector authorization"
				safe, _ := json.MarshalIndent(m, "", "  ")
				out.Content = string(safe)
			}
		}
	}
	sort.Strings(out.RedactedPaths)
	return out, nil
}
func redactControlSecrets(m map[string]any, prefix string, paths *[]string) {
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		l := strings.ToLower(k)
		if l == "headers" || l == "env" || l == "clientsecret" || l == "accesstoken" || l == "refreshtoken" || l == "authorization" || l == "token" || l == "password" || l == "secret" || l == "apikey" || l == "api_key" || l == "access_token" || l == "client_secret" {
			m[k] = "[REDACTED]"
			*paths = append(*paths, p)
			continue
		}
		switch n := v.(type) {
		case map[string]any:
			redactControlSecrets(n, p, paths)
		case []any:
			for i, x := range n {
				if sub, ok := x.(map[string]any); ok {
					redactControlSecrets(sub, fmt.Sprintf("%s.%d", p, i), paths)
				}
			}
		}
	}
}
func (s *ControlService) Validate(t ControlTarget, content string) error {
	_, rel, e := s.resolve(t)
	if e != nil {
		return e
	}
	if len(content) > maxControlContent || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return fmt.Errorf("content must be UTF-8 text of at most 1 MiB")
	}
	switch t.ResourceType {
	case "agent":
		if filepath.Base(rel) != "agent.yml" {
			return nil
		}
		if e = catalog.ValidateAgentCandidate(t.ResourceKey, []byte(content)); e != nil {
			return e
		}
		m, e := parseDefinition(content)
		if e != nil {
			return e
		}
		if s.Registry != nil {
			for _, name := range stringList(node(m, "toolConfig")["tools"]) {
				if _, ok := s.Registry.Tool(name); !ok {
					return fmt.Errorf("unknown tool %s", name)
				}
			}
			for _, name := range stringList(node(m, "skillConfig")["skills"]) {
				if _, ok := s.Registry.SkillDefinition(name); !ok {
					return fmt.Errorf("unknown skill %s", name)
				}
			}
			var packages []connector.Package
			ids, err := catalog.EffectiveConnectorIDs(m, s.Config.PresetConnectors)
			if err != nil {
				return err
			}
			for _, name := range ids {
				pkg, e := s.Config.Paths.ConnectorSources().Load(name)
				if e != nil {
					return fmt.Errorf("unavailable connector %s", name)
				}
				packages = append(packages, pkg)
			}
			if e := connector.ValidateSelection(packages); e != nil {
				return e
			}
		}
		if key, _ := node(m, "modelConfig")["modelKey"].(string); key != "" && s.Models != nil {
			if _, _, e := s.Models.Get(key); e != nil {
				return fmt.Errorf("unavailable model %s", key)
			}
		}
	case "team":
		team, e := catalog.ValidateTeamCandidate(t.ResourceKey, []byte(content))
		if e != nil {
			return e
		}
		if s.Registry != nil {
			for _, key := range team.AgentKeys {
				if _, ok := s.Registry.AgentDefinition(key); !ok {
					return fmt.Errorf("unavailable team member %s", key)
				}
			}
		}
	case "skill":
		if filepath.Base(rel) == "SKILL.md" {
			for _, d := range catalog.ValidateSkillCandidate(t.ResourceKey, []byte(content), s.Config.Skills.MaxPromptChars) {
				if d.Severity == "error" {
					return fmt.Errorf("%s", d.Message)
				}
			}
		}
	case "connector":
		return connector.ValidateManifest(t.ResourceKey, []byte(content))
	}
	return nil
}
func (s *ControlService) Prepare(c ControlChange, caller string) (*ControlPlan, error) {
	if c.Action != "apply" && c.Action != "delete" {
		return nil, fmt.Errorf("unsupported action")
	}
	if connector.IsBuiltin(c.ResourceKey) || c.ResourceType == "agent" && strings.EqualFold(c.ResourceKey, caller) {
		return nil, fmt.Errorf("protected resource")
	}
	if c.Action == "delete" && c.ResourceType == "team" {
		return nil, fmt.Errorf("team deletion is not supported")
	}
	root, rel, e := s.resolve(c.ControlTarget)
	if e != nil {
		return nil, e
	}
	if c.ResourceType == "agent" && safePart(caller) {
		targetInfo, targetErr := os.Stat(root)
		callerInfo, callerErr := os.Stat(filepath.Join(s.Config.Paths.AgentsDir, caller))
		if targetErr == nil && callerErr == nil && os.SameFile(targetInfo, callerInfo) {
			return nil, fmt.Errorf("protected resource")
		}
	}
	revision, exists, e := treeRevision(root)
	if e != nil {
		return nil, e
	}
	if exists && c.BaseRevision != revision || !exists && c.BaseRevision != "" || c.Action == "delete" && !exists {
		return nil, fmt.Errorf("revision_conflict: read the current resource before changing it")
	}
	p := &ControlPlan{Change: c, root: root, rel: rel, revision: revision, exists: exists}
	if c.MCPURL != "" {
		if c.Action != "apply" || c.ResourceType != "connector" || exists {
			return nil, fmt.Errorf("mcpUrl only supports creating a new HTTP MCP connector")
		}
		generated, err := controlMCPFile(c)
		if err != nil {
			return nil, err
		}
		p.GeneratedFiles = map[string]string{"mcp.json": generated}
	}
	old, e := os.ReadFile(filepath.Join(root, rel))
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if len(old) > 0 {
		view, e := s.Read(c.ControlTarget)
		if e != nil {
			return nil, e
		}
		if !view.Editable {
			return nil, fmt.Errorf("%s", view.Reason)
		}
		p.Before = view.Content
	}
	if c.Action == "apply" {
		if !exists {
			primary := map[string]string{"agent": "agent.yml", "team": "team.yml", "skill": "SKILL.md", "connector": "connector.json"}[c.ResourceType]
			if filepath.Base(rel) != primary {
				return nil, fmt.Errorf("create the primary definition first")
			}
		}
		if len(c.PreservePaths) > 0 {
			if c.ResourceType != "agent" || filepath.Base(rel) != "agent.yml" {
				return nil, fmt.Errorf("preservePaths only supports agent runtimeConfig.env keys")
			}
			c.Content, e = preserveAgentEnvironment(string(old), c.Content, c.PreservePaths)
			if e != nil {
				return nil, e
			}
			p.Change = c
		}
		if strings.Contains(c.Content, "[REDACTED]") {
			return nil, fmt.Errorf("unresolved redacted content; use preservePaths")
		}
		if e = s.Validate(c.ControlTarget, c.Content); e != nil {
			return nil, e
		}
		p.After = c.Content
		if c.ResourceType == "agent" && filepath.Base(rel) == "agent.yml" {
			p.After, _, e = redactAgentEnvironment(c.Content)
			if e != nil {
				return nil, e
			}
		}
		if c.ResourceType == "connector" {
			var definition map[string]any
			if e := json.Unmarshal([]byte(c.Content), &definition); e != nil {
				return nil, e
			}
			var paths []string
			redactControlSecrets(definition, "", &paths)
			if len(paths) > 0 {
				return nil, fmt.Errorf("inline credentials are not editable; use connector authorization")
			}
		}
	} else if e = s.checkReferences(c.ControlTarget); e != nil {
		return nil, e
	}
	if generated, ok := p.GeneratedFiles["mcp.json"]; ok {
		p.After += "\n\n--- generated mcp.json ---\n" + generated
	}
	encoded, _ := json.Marshal(struct {
		Change ControlChange
		Caller string
		Files  map[string]string
	}{c, caller, p.GeneratedFiles})
	p.Digest = controlHash(encoded)
	return p, nil
}
func (s *ControlService) checkReferences(t ControlTarget) error {
	if t.ResourceType == "agent" {
		err := filepath.WalkDir(s.Config.Paths.TeamsDir, func(path string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("cannot verify symlinked team source")
			}
			if d.IsDir() || d.Name() != "team.yml" && d.Name() != "team.yaml" {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			team, e := catalog.ValidateTeamCandidate(filepath.Base(filepath.Dir(path)), b)
			if e != nil {
				return fmt.Errorf("cannot verify invalid team source")
			}
			for _, key := range team.AgentKeys {
				if strings.EqualFold(key, t.ResourceKey) {
					return fmt.Errorf("agent is referenced by team %s", filepath.Base(filepath.Dir(path)))
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if t.ResourceType == "connector" {
		r, ok := s.Registry.(ConnectorUsageReader)
		if !ok {
			return fmt.Errorf("connector usage unavailable")
		}
		users, e := r.ConnectorUsers(t.ResourceKey)
		if e != nil {
			return e
		}
		if len(users) > 0 {
			return &ConnectorInUseError{users}
		}
	}
	// Scan current source as well as the published catalog, including invalid Agents.
	return filepath.WalkDir(s.Config.Paths.AgentsDir, func(path string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot verify symlinked agent source")
		}
		if d.IsDir() || d.Name() != "agent.yml" && d.Name() != "agent.yaml" {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		m, e := parseDefinition(string(b))
		if e != nil {
			return fmt.Errorf("cannot verify references in invalid agent source")
		}
		var refs []string
		if t.ResourceType == "skill" {
			refs = stringList(node(m, "skillConfig")["skills"])
		}
		for _, key := range refs {
			if strings.EqualFold(key, t.ResourceKey) {
				return fmt.Errorf("resource is referenced by %s", filepath.Base(filepath.Dir(path)))
			}
		}
		return nil
	})
}
func (s *ControlService) Apply(ctx context.Context, c ControlChange, caller, digest string) (map[string]any, error) {
	unlock := s.Mutations.LockAgentMutation()
	defer unlock()
	sourceUnlock := s.Mutations.LockSourceMutation()
	defer sourceUnlock()
	var result map[string]any
	reason := map[string]string{"agent": "agents", "team": "teams", "skill": "skills", "connector": "connectors"}[c.ResourceType]
	mutate := func(ctx context.Context) error {
		var err error
		result, err = s.applyLocked(ctx, c, caller, digest)
		return err
	}
	var err error
	if s.Coordinate != nil {
		err = s.Coordinate(ctx, reason, mutate)
	} else {
		err = mutate(ctx)
	}
	return result, err
}
func (s *ControlService) reload(ctx context.Context, kind string) error {
	reason := map[string]string{"agent": "agents", "team": "teams", "skill": "skills", "connector": "connectors"}[kind]
	if s.Reload != nil {
		return s.Reload(ctx, reason)
	}
	return s.Registry.Reload(ctx, "control")
}
func (s *ControlService) applyLocked(ctx context.Context, c ControlChange, caller, digest string) (out map[string]any, err error) {
	state := "not_started"
	defer func() {
		if err != nil {
			err = &contracts.MutationError{State: state, Err: err}
		}
	}()
	if c.ResourceType == "connector" {
		release, e := connector.AcquireOperation(s.Config.Paths.EffectiveConnectorsCenterDir(), c.ResourceKey)
		if e != nil {
			return nil, e
		}
		defer release()
	}
	p, e := s.Prepare(c, caller)
	if e != nil {
		return nil, e
	}
	if p.Digest != digest {
		return nil, fmt.Errorf("approval_stale")
	}
	if e = os.MkdirAll(filepath.Dir(p.root), 0755); e != nil {
		return nil, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(p.root), ".control-stage-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(stage)
	if p.exists {
		if e = copyControlTree(p.root, stage); e != nil {
			return nil, e
		}
	}
	removeWhole := c.Action == "delete" && !strings.Contains(c.ResourceKey, "/")
	target := filepath.Join(stage, p.rel)
	if c.Action == "apply" {
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return nil, e
		}
		if e = writeControlSource(target, []byte(p.Change.Content)); e != nil {
			return nil, e
		}
	} else if !removeWhole {
		if e = os.RemoveAll(filepath.Dir(target)); e != nil {
			return nil, e
		}
	}
	if c.ResourceType == "skill" && strings.Contains(c.ResourceKey, "/") {
		if e = updateControlPackage(stage, strings.Split(c.ResourceKey, "/")[1], c.Action == "delete"); e != nil {
			return nil, e
		}
	}
	if c.ResourceType == "connector" && c.Action == "apply" {
		for name, content := range p.GeneratedFiles {
			if e = writeControlSource(filepath.Join(stage, name), []byte(content)); e != nil {
				return nil, e
			}
		}
		if _, e = connector.LoadDirectory(stage, c.ResourceKey); e != nil {
			return nil, e
		}
	}
	if !removeWhole {
		mode := os.FileMode(0755)
		if p.exists {
			info, err := os.Stat(p.root)
			if err != nil {
				return nil, err
			}
			mode = info.Mode().Perm()
		}
		if err := os.Chmod(stage, mode); err != nil {
			return nil, err
		}
	}
	// Recheck immediately before publication; external editors do not share our mutex.
	latest, _, e := treeRevision(p.root)
	if e != nil {
		return nil, e
	}
	if latest != p.revision {
		return nil, fmt.Errorf("revision_conflict")
	}
	backup := stage + "-backup"
	state = "unknown"
	if p.exists {
		if e = os.Rename(p.root, backup); e != nil {
			return nil, e
		}
	}
	published := false
	rollback := func(cause error) (map[string]any, error) {
		if published {
			if e := os.RemoveAll(p.root); e != nil {
				return nil, fmt.Errorf("rollback_failed: %v; %w", e, cause)
			}
		}
		if p.exists {
			if e := os.Rename(backup, p.root); e != nil {
				return nil, fmt.Errorf("rollback_failed: %v; %w", e, cause)
			}
		}
		if e := s.reload(context.WithoutCancel(ctx), c.ResourceType); e != nil {
			return nil, fmt.Errorf("rollback_reload_failed: %w", e)
		}
		state = "rolled_back"
		return map[string]any{"status": "rolled_back", "diagnostics": []string{cause.Error()}}, fmt.Errorf("change failed; previous state restored: %w", cause)
	}
	if !removeWhole {
		if e = os.Rename(stage, p.root); e != nil {
			return rollback(e)
		}
		published = true
	}
	if e = s.reload(context.WithoutCancel(ctx), c.ResourceType); e != nil {
		return rollback(e)
	}
	state = "committed"
	if p.exists {
		if e = os.RemoveAll(backup); e != nil {
			return nil, fmt.Errorf("source published but backup cleanup failed: %w", e)
		}
	}
	status := "applied"
	if c.Action == "apply" && c.ResourceType == "agent" {
		if _, ok := s.Registry.AgentDefinition(c.ResourceKey); !ok {
			status = "invalid"
		}
		if r, ok := s.Registry.(interface{ RuntimePublicationPending(string) bool }); ok && r.RuntimePublicationPending(c.ResourceKey) {
			status = "pending"
		}
	}
	revision, _, e := treeRevision(p.root)
	if e != nil {
		return nil, e
	}
	return map[string]any{"status": status, "resourceType": c.ResourceType, "resourceKey": c.ResourceKey, "baseRevision": revision}, nil
}
func copyControlTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected")
		}
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if err = os.MkdirAll(out, info.Mode().Perm()); err != nil {
				return err
			}
			if rel != "." {
				return os.Chmod(out, info.Mode().Perm())
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular file rejected")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
}
func updateControlPackage(dir, member string, remove bool) error {
	path := filepath.Join(dir, "package.json")
	b, e := os.ReadFile(path)
	if e != nil {
		return fmt.Errorf("package membership requires an existing package.json: %w", e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	items, ok := m["skills"].([]any)
	if !ok {
		return fmt.Errorf("invalid package skills")
	}
	next := []any{}
	found := false
	for _, item := range items {
		v, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid package member")
		}
		if v["id"] == member {
			found = true
			if remove {
				continue
			}
		}
		next = append(next, item)
	}
	if !remove && !found {
		next = append(next, map[string]any{"id": member})
	}
	m["skills"] = next
	b, e = json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	return writeControlSource(path, append(b, '\n'))
}

// Existing source permissions survive replacement; private state has separate writers.
func writeControlSource(path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// controlMCPFile creates only the standard HTTP component. Authentication remains
// in connectorauth: auth_mode=mcp uses protected-resource / authorization-server discovery.
func controlMCPFile(c ControlChange) (string, error) {
	u, err := url.Parse(c.MCPURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("mcpUrl must be an absolute HTTP(S) URL without userinfo or fragment")
	}
	if err := connector.ValidateManifest(c.ResourceKey, []byte(c.Content)); err != nil {
		return "", err
	}
	var m connector.Manifest
	if err := connector.DecodeJSON([]byte(c.Content), &m); err != nil {
		return "", err
	}
	if m.Type != "mcp" || (m.AuthMode != connector.AuthMCP && m.AuthMode != connector.AuthNoAuth) {
		return "", fmt.Errorf("HTTP MCP creation requires type=mcp and auth_mode=mcp (OAuth discovery) or no_auth")
	}
	components := map[string]map[string]any{"main": {"type": "http", "url": c.MCPURL}}
	// Match login's local validation before asking the user to approve creation.
	if err := connectorauth.ValidatePackage(connector.Package{Manifest: m, MCP: components}); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": components}, "", "  ")
	return string(data) + "\n", err
}
