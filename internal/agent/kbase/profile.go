package kbase

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const (
	Mode            = "KBASE"
	MainStage       = "kbase"
	MainCacheKey    = "kbase:main"
	CreatePrefix    = "kbase"
	DefaultIconName = "kbase"

	ToolSearch   = "kbase_search"
	ToolFiles    = "kbase_files"
	ToolRead     = "kbase_read"
	ToolStatus   = "kbase_status"
	ToolRefresh  = "kbase_refresh"
	ToolDatetime = "datetime"
)

const DefaultSystemPrompt = `KBASE Mode
You answer using the workspace knowledge base for this agent.

Rules:
- Search the knowledge base with kbase_search before answering factual questions about the indexed workspace.
- Use kbase_files when you need to discover indexed files or browse nearby indexed paths.
- Base answers on retrieved evidence. If the available evidence is insufficient, say that the knowledge base does not contain enough information.
- Cite source paths and line ranges from kbase_search or kbase_read when giving concrete claims.
- Use kbase_read when a search result needs more surrounding context.
- Do not claim that unindexed or missing files were searched.`

var defaultToolNames = []string{
	ToolSearch,
	ToolFiles,
	ToolRead,
	ToolStatus,
	ToolRefresh,
	ToolDatetime,
}

var defaultContextTags = []string{"system", "session"}

var defaultBudget = map[string]any{
	"timeout":  900,
	"maxSteps": 40,
	"tool": map[string]any{
		"maxCalls": 80,
	},
}

func DefaultToolNames() []string {
	return append([]string(nil), defaultToolNames...)
}

func DefaultContextTags() []string {
	return append([]string(nil), defaultContextTags...)
}

func DefaultBudget() map[string]any {
	return contracts.CloneMap(defaultBudget)
}

func Descriptor() agentcontract.ModeDescriptor {
	return agentcontract.ModeDescriptor{
		Mode:         Mode,
		MainStage:    MainStage,
		MainCacheKey: MainCacheKey,
		CreatePrefix: CreatePrefix,
		Profile: agentcontract.ModeProfile{
			IconName:    DefaultIconName,
			ToolNames:   DefaultToolNames(),
			ContextTags: DefaultContextTags(),
			Budget:      DefaultBudget(),
		},
		Capabilities: agentcontract.ModeCapabilities{
			RunAsChild: true,
		},
	}
}

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              MainCacheKey,
		FingerprintStage:      MainStage,
		PromptStage:           MainStage,
		Mode:                  MainStage,
		Stage:                 "main",
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !strings.EqualFold(strings.TrimSpace(session.Mode), Mode) ||
		!strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	prompt := strings.TrimSpace(session.ModeSystemPrompt)
	if prompt == "" {
		prompt = DefaultSystemPrompt
	}
	if len(toolNames) == 0 {
		toolNames = session.ToolNames
	}
	workspaceDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.WorkspaceDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
		session.WorkspaceRoot,
	)
	chatDir := agentcontract.FirstNonBlank(
		session.RuntimeContext.LocalPaths.ChatAttachmentsDir,
		session.RuntimeContext.SandboxPaths.WorkspaceDir,
	)
	values := agentcontract.CommonPromptValues(agentcontract.PromptContext{
		AgentKey:       session.AgentKey,
		AgentName:      session.AgentName,
		Mode:           session.Mode,
		PlanningMode:   session.PlanningMode,
		WorkspaceDir:   workspaceDir,
		ChatDir:        chatDir,
		AvailableTools: toolNames,
		UserRequest:    req.Message,
	})
	return agentcontract.RenderPromptTemplate(prompt, values)
}

func IsTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ToolSearch, ToolFiles, ToolRead, ToolStatus, ToolRefresh, ToolDatetime:
		return true
	default:
		return false
	}
}

func FilterTools(tools []string) []string {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(tools))
	for _, tool := range tools {
		if IsTool(tool) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// BoundaryPolicy is the KBASE-owned runtime boundary consumed by catalog's
// YAML adapter. KBASE never carries memory state, and configured tools are
// constrained to the KBASE allowlist with the mode defaults as fallback.
type BoundaryPolicy struct {
	ToolNames     []string
	MemoryEnabled bool
}

func ResolveBoundaryPolicy(toolNames []string) BoundaryPolicy {
	filtered := FilterTools(toolNames)
	if len(filtered) == 0 {
		filtered = DefaultToolNames()
	}
	return BoundaryPolicy{ToolNames: filtered, MemoryEnabled: false}
}
