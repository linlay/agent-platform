package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbase"
)

var ErrInvalidAgentSummaryScope = errors.New("invalid agent summary scope")

type Registry interface {
	Agents(tag string) []api.AgentSummary
	Teams() []api.TeamSummary
	Skills(tag string) []api.SkillSummary
	SkillDefinition(key string) (SkillDefinition, bool)
	Tools(kind string, tag string) []api.ToolSummary
	Tool(name string) (api.ToolDetailResponse, bool)
	DefaultAgentKey() string
	AgentDefinition(key string) (AgentDefinition, bool)
	TeamDefinition(teamID string) (TeamDefinition, bool)
	Reload(ctx context.Context, reason string) error
}

// TeamResolver is the atomic team view used by run admission and automation.
// It is kept separate from Registry so narrow test and integration registries
// that do not participate in team-scoped execution need not implement it.
type TeamResolver interface {
	ResolveTeam(teamID string) (TeamSnapshot, bool)
}

type AgentDefinition struct {
	Key              string
	Name             string
	Icon             any
	Description      string
	Role             string
	Greetings        []string
	Wonders          []string
	ModelKey         string
	ServiceTier      string
	Mode             string
	ACPBridgeID      string
	VisibilityScopes []string
	Tools            []string
	Skills           []string
	Controls         []map[string]any
	Runtime          map[string]any
	HostAccess       AgentHostAccessConfig
	Workspace        AgentWorkspaceConfig
	Project          AgentProjectConfig
	KBaseConfig      kbase.AgentConfig
	ContextTags      []string
	ContextAgents    []string
	Budget           map[string]any
	StageSettings    map[string]any
	RuntimePrompts   AgentRuntimePrompts
	AgentDir         string

	// PROXY mode: forward /api/query to a remote AGW-compatible service.
	ProxyConfig *ProxyConfig

	// CHANNEL mode / exports: import remote agents or expose native agents through channels.
	ChannelConfig AgentChannelConfig

	// Prompt files loaded from agent directory.
	SoulPrompt   string // from SOUL.md
	AgentsPrompt string // resolved from promptFile or AGENTS.md fallback

	// PLAN_EXECUTE stage prompts (stage-scoped promptFile).
	PlanPrompt         string
	ExecutePrompt      string
	SummaryPrompt      string
	StaticMemoryPrompt string
	MemoryEnabled      bool
	MemoryConfig       AgentMemoryConfig
}

type AgentWorkspaceConfig struct {
	Root string
}

type AgentHostAccessConfig struct {
	ReadRoots  []string
	WriteRoots []string
}

type AgentProjectConfig struct {
	PromptFiles []AgentProjectPromptFile
	Git         AgentProjectGitConfig
}

type AgentProjectPromptFile struct {
	Source string
	Path   string
}

type AgentProjectGitConfig struct {
	ExpectedBranch string
}

type AgentMemoryConfig struct {
	Enabled         bool
	ManagementTools bool
	Embedding       AgentMemoryEmbeddingConfig
	AutoRemember    AgentMemoryAutoRememberConfig
}

type AgentMemoryEmbeddingConfig struct {
	ProviderKey string
	Model       string
	Dimension   int
	Timeout     int
}

type AgentMemoryAutoRememberConfig struct {
	Enabled  bool
	ModelKey string
	Timeout  int64
}

type AgentRuntimePrompts struct {
	Skill        SkillPromptConfig
	ToolAppendix ToolAppendixPromptConfig
}

// ProxyConfig configures PROXY mode: forward /api/query to a remote
// AGW-compatible service (e.g. claude-code relay-server on port 3210).
type ProxyConfig struct {
	BaseURL      string // e.g. http://127.0.0.1:3210
	WebSocketURL string // optional direct websocket endpoint for CHANNEL imports
	Transport    string // ws or sse; defaults to ws for bidirectional run control
	Protocol     string // agw-platform or platform-ws
	AgentKey     string // optional upstream agentKey override
	ChannelID    string // optional inbound server-mode channel to reuse for CHANNEL imports
	ChatID       string // optional upstream chatId override
	Token        string // optional Bearer token
	TokenEnv     string // optional env var name for Bearer token
	Timeout      int    // default 300 (5 min), seconds
	TimeoutMS    int    // ACP bridge override, milliseconds
}

type AgentChannelConfig struct {
	ChannelID      string
	RemoteAgentKey string
	Exports        []AgentChannelExport
}

type AgentChannelExport struct {
	ChannelID        string
	ExternalAgentKey string
	Allow            AgentChannelAllow
}

type AgentChannelAllow struct {
	Query        bool
	Submit       bool
	Steer        bool
	Interrupt    bool
	FileTransfer bool
}

type SkillPromptConfig struct {
	CatalogHeader     string
	DisclosureHeader  string
	InstructionsLabel string
}

type ToolAppendixPromptConfig struct {
	ToolDescriptionTitle string
	AfterCallHintTitle   string
}

type TeamDefinition struct {
	TeamID       string
	Name         string
	Description  string
	Icon         any
	AgentKeys    []string
	RuntimeMode  string
	Orchestrator TeamOrchestratorConfig
	SoulPrompt   string
	AgentsPrompt string
	TeamDir      string
}

const (
	TeamRuntimeModeOrchestrated = "orchestrated"
	TeamDefaultMaxParallel      = 5
)

// TeamOrchestratorConfig is the catalog-owned, immutable configuration used
// to synthesize the hidden TEAM runtime agent. It deliberately contains no
// public agent key and is never registered in the ordinary agent catalog.
type TeamOrchestratorConfig struct {
	ModelKey      string
	ServiceTier   string
	Budget        map[string]any
	MaxParallel   int
	StageSettings map[string]any
}

// TeamSnapshot is the immutable, run-scoped result of resolving a team and
// its referenced agents under one catalog read lock. Callers must use this
// snapshot for the lifetime of a run instead of consulting the hot-reloaded
// registry again.
type TeamSnapshot struct {
	TeamID                  string
	Name                    string
	Description             string
	Icon                    any
	AgentKeys               []string
	ValidAgentKeys          []string
	InvalidAgentKeys        []string
	RuntimeMode             string
	Orchestrator            TeamOrchestratorConfig
	SoulPrompt              string
	AgentsPrompt            string
	RosterFingerprint       string
	ToolSchemaFingerprint   string
	OrchestratorFingerprint string

	agentDefinitions map[string]AgentDefinition
}

func (s TeamSnapshot) HasAgent(agentKey string) bool {
	agentKey = strings.TrimSpace(agentKey)
	for _, key := range s.ValidAgentKeys {
		if key == agentKey {
			return true
		}
	}
	return false
}

func (s TeamSnapshot) DeclaresAgent(agentKey string) bool {
	agentKey = strings.TrimSpace(agentKey)
	for _, key := range s.AgentKeys {
		if key == agentKey {
			return true
		}
	}
	return false
}

// AgentDefinition returns the member definition frozen when the team was
// resolved. The returned value is cloned so callers cannot mutate the
// run-scoped snapshot through maps, slices, or pointer fields.
func (s TeamSnapshot) AgentDefinition(agentKey string) (AgentDefinition, bool) {
	def, ok := s.agentDefinitions[strings.TrimSpace(agentKey)]
	if !ok {
		return AgentDefinition{}, false
	}
	return cloneAgentDefinitionSnapshot(def), true
}

type SkillDefinition struct {
	Key             string
	Name            string
	Description     string
	Triggers        []string
	Metadata        map[string]any
	Prompt          string
	PromptTruncated bool
	BashHooksDir    string
	RuntimeEnv      map[string]string
}

type FileRegistry struct {
	cfg   config.Config
	tools []api.ToolDetailResponse

	mu          sync.RWMutex
	agents      map[string]AgentDefinition
	adminAgents map[string]AdminAgent
	// runtimeInvalidAgents records startup-only runtime validation failures
	// (currently incompatible KBASE control stores). A catalog reload must not
	// resurrect one of these agents or try to adopt its storage again.
	runtimeInvalidAgents map[string]AdminAgentDiagnostic
	teams                map[string]TeamDefinition
	skills               map[string]SkillDefinition
}

func NewFileRegistry(cfg config.Config, toolDefs []api.ToolDetailResponse) (*FileRegistry, error) {
	registry := &FileRegistry{
		cfg:                  cfg,
		tools:                dedupeToolDefinitions(append([]api.ToolDetailResponse(nil), toolDefs...)),
		agents:               map[string]AgentDefinition{},
		adminAgents:          map[string]AdminAgent{},
		runtimeInvalidAgents: map[string]AdminAgentDiagnostic{},
		teams:                map[string]TeamDefinition{},
		skills:               map[string]SkillDefinition{},
	}
	if err := registry.Reload(context.Background(), "startup"); err != nil {
		return nil, err
	}
	return registry, nil
}

// Reload reloads catalog entries scoped by reason. Supported reasons:
//
//	"startup" / "" / "config" — reload everything
//	"agents" — reload only agents
//	"teams"  — reload only teams
//	"skills" — reload only skills
//
// Other reasons fall through to a full reload.
func (r *FileRegistry) Reload(_ context.Context, reason string) error {
	switch reason {
	case "agents":
		agents, adminAgents, err := loadAgentsWithAdmin(r.cfg.Paths.AgentsDir, r.cfg.Paths.SkillsMarketDir, r.cfg.Memory.Enabled)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.applyRuntimeInvalidAgentsLocked(agents, adminAgents)
		r.agents = agents
		r.adminAgents = adminAgents
		r.mu.Unlock()
		return nil
	case "teams":
		teams, err := loadTeams(r.cfg.Paths.TeamsDir)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.teams = teams
		r.mu.Unlock()
		return nil
	case "skills":
		skills, err := loadSkills(r.cfg.Paths.SkillsMarketDir, r.cfg.Skills.MaxPromptChars)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.skills = skills
		r.mu.Unlock()
		return nil
	}

	// Full reload (startup, config, or unknown reason)
	agents, adminAgents, err := loadAgentsWithAdmin(r.cfg.Paths.AgentsDir, r.cfg.Paths.SkillsMarketDir, r.cfg.Memory.Enabled)
	if err != nil {
		return err
	}
	teams, err := loadTeams(r.cfg.Paths.TeamsDir)
	if err != nil {
		return err
	}
	skills, err := loadSkills(r.cfg.Paths.SkillsMarketDir, r.cfg.Skills.MaxPromptChars)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.applyRuntimeInvalidAgentsLocked(agents, adminAgents)
	r.agents = agents
	r.adminAgents = adminAgents
	r.teams = teams
	r.skills = skills
	r.mu.Unlock()
	return nil
}

// InvalidateRuntimeAgent removes a syntactically valid agent from runtime
// resolution while retaining it as an invalid admin entry with a precise
// diagnostic. The state remains effective for the current process lifetime,
// including catalog hot reloads; the next process startup validates it again.
func (r *FileRegistry) InvalidateRuntimeAgent(key, code string, cause error) bool {
	if r == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	diagnostic := AdminAgentDiagnostic{
		Severity: "error",
		Code:     strings.TrimSpace(code),
		Message:  strings.TrimSpace(errorMessage(cause)),
	}
	if diagnostic.Code == "" {
		diagnostic.Code = "invalid_runtime_storage"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimeInvalidAgents == nil {
		r.runtimeInvalidAgents = map[string]AdminAgentDiagnostic{}
	}
	r.runtimeInvalidAgents[key] = diagnostic
	return r.applyRuntimeInvalidAgentLocked(key, diagnostic)
}

func (r *FileRegistry) applyRuntimeInvalidAgentsLocked(agents map[string]AgentDefinition, adminAgents map[string]AdminAgent) {
	for key, diagnostic := range r.runtimeInvalidAgents {
		r.applyRuntimeInvalidAgentToMapsLocked(key, diagnostic, agents, adminAgents)
	}
}

func (r *FileRegistry) applyRuntimeInvalidAgentLocked(key string, diagnostic AdminAgentDiagnostic) bool {
	return r.applyRuntimeInvalidAgentToMapsLocked(key, diagnostic, r.agents, r.adminAgents)
}

func (r *FileRegistry) applyRuntimeInvalidAgentToMapsLocked(key string, diagnostic AdminAgentDiagnostic, agents map[string]AgentDefinition, adminAgents map[string]AdminAgent) bool {
	definition, wasActive := agents[key]
	delete(agents, key)
	admin, exists := adminAgents[key]
	if !exists {
		if !wasActive {
			return false
		}
		admin = AdminAgent{Key: key, Name: definition.Name, Mode: definition.Mode, Status: AdminAgentStatusReady}
	}
	if diagnostic.SourcePath == "" {
		diagnostic.SourcePath = admin.Source.Path
	}
	admin.Status = AdminAgentStatusInvalid
	if !hasAdminDiagnostic(admin.Diagnostics, diagnostic.Code, diagnostic.Message) {
		admin.Diagnostics = append(admin.Diagnostics, diagnostic)
	}
	adminAgents[key] = admin
	return true
}

func hasAdminDiagnostic(items []AdminAgentDiagnostic, code, message string) bool {
	for _, item := range items {
		if item.Code == code && item.Message == message {
			return true
		}
	}
	return false
}

func errorMessage(err error) string {
	if err == nil {
		return "runtime validation failed"
	}
	return err.Error()
}

func (r *FileRegistry) AdminAgents() []AdminAgent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := r.orderedAdminAgentKeysLocked()
	items := make([]AdminAgent, 0, len(keys))
	for _, key := range keys {
		items = append(items, cloneAdminAgent(r.adminAgents[key]))
	}
	return items
}

func (r *FileRegistry) AdminAgent(key string) (AdminAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.adminAgents[strings.TrimSpace(key)]
	if !ok {
		return AdminAgent{}, false
	}
	return cloneAdminAgent(def), true
}

func (r *FileRegistry) AdminAgentKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedKeys(r.adminAgents)
}

func (r *FileRegistry) Agents(scope string) []api.AgentSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := r.orderedAgentKeysLocked()
	items := make([]api.AgentSummary, 0, len(keys))
	scope, err := NormalizeAgentSummaryScope(scope)
	if err != nil {
		return nil
	}
	for _, key := range keys {
		def := r.agents[key]
		if !AgentVisibleForScope(def, scope) {
			continue
		}
		apiMode := AgentModeForAPI(def.Mode)
		summary := api.AgentSummary{
			Key:          def.Key,
			Name:         def.Name,
			Icon:         def.Icon,
			Mode:         apiMode,
			WorkspaceDir: def.Workspace.Root,
			Description:  def.Description,
			Role:         def.Role,
		}
		if strings.EqualFold(strings.TrimSpace(def.Mode), AgentModeCoder) {
			summary.DefaultModelKey, summary.DefaultReasoningEffort = agentSummaryCoderDefaults(def)
		}
		items = append(items, summary)
	}
	return items
}

func agentSummaryCoderDefaults(def AgentDefinition) (string, string) {
	settings := contracts.ResolveCoderPlanningSettings(def.StageSettings, 0)
	modelKey := firstNonBlankString(
		settings.Execute.ModelKey,
		settings.Planning.ModelKey,
		def.ModelKey,
	)
	reasoningEffort := firstNonBlankString(
		settings.Execute.ReasoningEffort,
		settings.Planning.ReasoningEffort,
	)
	if strings.TrimSpace(reasoningEffort) == "" && agentSummaryReasoningDisabled(def.StageSettings) {
		reasoningEffort = "NONE"
	}
	if strings.TrimSpace(reasoningEffort) == "" {
		reasoningEffort = "MEDIUM"
	}
	return modelKey, reasoningEffort
}

func agentSummaryReasoningDisabled(raw map[string]any) bool {
	for _, key := range []string{"execute", "planning"} {
		node := contracts.AnyMapNode(raw[key])
		modelConfig := contracts.AnyMapNode(node["modelConfig"])
		reasoning := contracts.AnyMapNode(modelConfig["reasoning"])
		if enabled, ok := reasoning["enabled"].(bool); ok && !enabled {
			return true
		}
	}
	return false
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func NormalizeAgentSummaryScope(scope string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	if normalized == "" {
		return "all", nil
	}
	switch normalized {
	case "nav", "copilot", "invoke", "internal", "all":
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: %q (allowed: nav, copilot, invoke, internal, all)", ErrInvalidAgentSummaryScope, scope)
	}
}

func AgentVisibleForScope(def AgentDefinition, scope string) bool {
	scope, err := NormalizeAgentSummaryScope(scope)
	if err != nil {
		return false
	}
	if scope == "all" {
		return true
	}
	scopes := EffectiveAgentVisibilityScopes(def)
	return containsString(scopes, scope)
}

func AgentInvocable(def AgentDefinition) bool {
	scopes := EffectiveAgentVisibilityScopes(def)
	return containsString(scopes, "invoke") || containsString(scopes, "internal")
}

func EffectiveAgentVisibilityScopes(def AgentDefinition) []string {
	if len(def.VisibilityScopes) == 0 {
		return append([]string(nil), defaultAgentVisibilityScopes...)
	}
	return append([]string(nil), def.VisibilityScopes...)
}

func projectPromptFilesMeta(files []AgentProjectPromptFile) []map[string]any {
	if len(files) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"source": file.Source,
			"path":   file.Path,
		})
	}
	return out
}

func hasRuntimeSandboxDefinition(runtime map[string]any) bool {
	if len(runtime) == 0 {
		return false
	}
	environmentID, _ := runtime["environmentId"].(string)
	return strings.TrimSpace(environmentID) != ""
}

func normalizeProxyTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sse":
		return "sse"
	case "ws", "websocket":
		return "ws"
	default:
		return "ws"
	}
}

func runtimeSandboxSummaryMeta(runtime map[string]any) map[string]any {
	out := map[string]any{
		"environmentId": strings.TrimSpace(stringNode(runtime["environmentId"])),
		"level":         strings.ToUpper(strings.TrimSpace(stringNode(runtime["level"]))),
	}
	if mounts := listMaps(runtime["sandboxMounts"]); len(mounts) > 0 {
		out["sandboxMounts"] = cloneListMaps(mounts)
	}
	return out
}

func (r *FileRegistry) SkillDefinition(key string) (SkillDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.skills[key]
	return def, ok
}

func (r *FileRegistry) Tool(name string) (api.ToolDetailResponse, bool) {
	needle := strings.TrimSpace(strings.ToLower(name))
	for _, tool := range r.tools {
		if strings.ToLower(tool.Name) == needle || strings.ToLower(tool.Key) == needle {
			return api.ToolDetailResponse{
				Key:           tool.Key,
				Name:          tool.Name,
				Label:         tool.Label,
				Description:   tool.Description,
				AfterCallHint: tool.AfterCallHint,
				Parameters:    contracts.CloneMap(tool.Parameters),
				Meta:          contracts.CloneMap(tool.Meta),
			}, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (r *FileRegistry) DefaultAgentKey() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := sortedKeys(r.agents)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func (r *FileRegistry) AgentDefinition(key string) (AgentDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.agents[key]
	return def, ok
}

func (r *FileRegistry) TeamDefinition(teamID string) (TeamDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.teams[teamID]
	if !ok {
		return TeamDefinition{}, false
	}
	return cloneTeamDefinitionSnapshot(def), true
}

// ResolveTeam atomically resolves a team definition together with the current
// agent catalog. Member keys are normalized and de-duplicated while preserving
// declaration order.
func (r *FileRegistry) ResolveTeam(teamID string) (TeamSnapshot, bool) {
	teamID = strings.TrimSpace(teamID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	team, ok := r.teams[teamID]
	if !ok {
		return TeamSnapshot{}, false
	}
	return resolveTeamSnapshotLocked(team, r.agents), true
}

func resolveTeamSnapshotLocked(team TeamDefinition, agents map[string]AgentDefinition) TeamSnapshot {
	snapshot := TeamSnapshot{
		TeamID:           strings.TrimSpace(team.TeamID),
		Name:             strings.TrimSpace(team.Name),
		Description:      strings.TrimSpace(team.Description),
		Icon:             cloneAgentSnapshotValue(team.Icon),
		RuntimeMode:      TeamRuntimeModeOrchestrated,
		Orchestrator:     cloneTeamOrchestratorConfig(team.Orchestrator),
		SoulPrompt:       strings.TrimSpace(team.SoulPrompt),
		AgentsPrompt:     strings.TrimSpace(team.AgentsPrompt),
		agentDefinitions: make(map[string]AgentDefinition),
	}
	seen := make(map[string]struct{}, len(team.AgentKeys))
	for _, raw := range team.AgentKeys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		snapshot.AgentKeys = append(snapshot.AgentKeys, key)
		if def, exists := agents[key]; exists && strings.TrimSpace(def.Key) != "" {
			snapshot.ValidAgentKeys = append(snapshot.ValidAgentKeys, key)
			snapshot.agentDefinitions[key] = cloneAgentDefinitionSnapshot(def)
		} else {
			snapshot.InvalidAgentKeys = append(snapshot.InvalidAgentKeys, key)
		}
	}

	snapshot.RosterFingerprint = teamRosterFingerprint(snapshot)
	snapshot.ToolSchemaFingerprint = teamHiddenToolSchemaFingerprint(snapshot)
	snapshot.OrchestratorFingerprint = teamOrchestratorFingerprint(snapshot)
	return snapshot
}

// NewTeamSnapshot resolves a standalone team definition against an explicit
// agent set. FileRegistry.ResolveTeam performs the same operation atomically
// under its catalog read lock; this constructor is for narrow registry
// adapters and tests that cannot implement that atomic interface.
func NewTeamSnapshot(team TeamDefinition, agents map[string]AgentDefinition) TeamSnapshot {
	return resolveTeamSnapshotLocked(team, agents)
}

func cloneTeamOrchestratorConfig(src TeamOrchestratorConfig) TeamOrchestratorConfig {
	dst := src
	dst.Budget = cloneAgentSnapshotMap(src.Budget)
	dst.StageSettings = cloneAgentSnapshotMap(src.StageSettings)
	return dst
}

func cloneTeamDefinitionSnapshot(src TeamDefinition) TeamDefinition {
	dst := src
	dst.Icon = cloneAgentSnapshotValue(src.Icon)
	dst.AgentKeys = append([]string(nil), src.AgentKeys...)
	dst.Orchestrator = cloneTeamOrchestratorConfig(src.Orchestrator)
	return dst
}

func cloneAgentDefinitionSnapshot(src AgentDefinition) AgentDefinition {
	dst := src
	dst.Icon = cloneAgentSnapshotValue(src.Icon)
	dst.Greetings = append([]string(nil), src.Greetings...)
	dst.Wonders = append([]string(nil), src.Wonders...)
	dst.VisibilityScopes = append([]string(nil), src.VisibilityScopes...)
	dst.Tools = append([]string(nil), src.Tools...)
	dst.Skills = append([]string(nil), src.Skills...)
	if len(src.Controls) > 0 {
		dst.Controls = make([]map[string]any, 0, len(src.Controls))
		for _, control := range src.Controls {
			dst.Controls = append(dst.Controls, cloneAgentSnapshotMap(control))
		}
	} else {
		dst.Controls = nil
	}
	dst.Runtime = cloneAgentSnapshotMap(src.Runtime)
	dst.HostAccess.ReadRoots = append([]string(nil), src.HostAccess.ReadRoots...)
	dst.HostAccess.WriteRoots = append([]string(nil), src.HostAccess.WriteRoots...)
	dst.Project.PromptFiles = append([]AgentProjectPromptFile(nil), src.Project.PromptFiles...)
	dst.KBaseConfig.Include = append([]string(nil), src.KBaseConfig.Include...)
	dst.KBaseConfig.Exclude = append([]string(nil), src.KBaseConfig.Exclude...)
	dst.KBaseConfig.Tags = append([]string(nil), src.KBaseConfig.Tags...)
	dst.ContextTags = append([]string(nil), src.ContextTags...)
	dst.ContextAgents = append([]string(nil), src.ContextAgents...)
	dst.Budget = cloneAgentSnapshotMap(src.Budget)
	dst.StageSettings = cloneAgentSnapshotMap(src.StageSettings)
	if src.ProxyConfig != nil {
		proxy := *src.ProxyConfig
		dst.ProxyConfig = &proxy
	}
	dst.ChannelConfig = cloneAgentChannelConfig(src.ChannelConfig)
	return dst
}

func cloneAgentSnapshotMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = cloneAgentSnapshotValue(value)
	}
	return dst
}

func cloneAgentSnapshotValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAgentSnapshotMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneAgentSnapshotValue(item)
		}
		return cloned
	case []map[string]any:
		cloned := make([]map[string]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneAgentSnapshotMap(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		return contracts.CloneStringMap(typed)
	default:
		return value
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make([]map[string]any, 0, len(src))
	for _, item := range src {
		dst = append(dst, contracts.CloneMap(item))
	}
	return dst
}

func dedupeToolDefinitions(src []api.ToolDetailResponse) []api.ToolDetailResponse {
	if len(src) == 0 {
		return nil
	}
	out := make([]api.ToolDetailResponse, 0, len(src))
	seen := map[string]struct{}{}
	for _, tool := range src {
		dedupeKey := strings.ToLower(strings.TrimSpace(tool.Key)) + "|" + strings.ToLower(strings.TrimSpace(tool.Name))
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}
		out = append(out, tool)
	}
	return out
}

func stringNode(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	default:
		return ""
	}
}

func intNode(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func floatNode(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func mapNode(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func listStrings(value any) []string {
	switch v := value.(type) {
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if text := stringNode(item); text != "" {
				items = append(items, text)
			}
		}
		return items
	case []string:
		return append([]string(nil), v...)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	default:
		return nil
	}
}

func listMaps(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			result = append(result, entry)
		}
	}
	return result
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func skillDisplayName(name string, description string, fallback string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if strings.TrimSpace(description) != "" {
		return strings.TrimSpace(description)
	}
	return fallback
}
