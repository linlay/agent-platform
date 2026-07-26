package kbase

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	corekbase "agent-platform/internal/kbase"
)

const DefaultModePrompt = "KBASE Mode\nYou are a dedicated knowledge-base question-answering agent."

const DefaultSystemPrompt = corekbase.DefaultCapabilityPrompt + "\n\n" + DefaultModePrompt

const DefaultEditingPrompt = `KBASE Editing Mode
The user explicitly enabled editing for this run.

Rules:
- Only modify Markdown files ending in .md within {{kbase_source_root}}.
- Never modify .markdown files, non-Markdown files, directories, files outside the knowledge source, or paths that escape through symlinks.
- Make only changes required by the user's request. Do not perform opportunistic cleanup.
- Read an existing file with file_read before changing it. Prefer file_edit for localized changes and file_write only for a complete replacement or a new file.
- Use file_glob and file_grep only for .md files under the knowledge source.
- Do not use shell commands or other tools to bypass the editing boundary.
- A successful file mutation includes a kbase-index hook result. Only claim that the change is searchable when that hook reports success.
- If the file was saved but indexing failed or was skipped, report that distinction clearly and use kbase_refresh at most once to retry a failed index update.
- In the final answer, list every changed path and summarize its lineStats.`

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	if !IsMode(session.Mode) {
		return ""
	}
	editing := strings.EqualFold(strings.TrimSpace(stage), EditingStage)
	if !editing && !strings.EqualFold(strings.TrimSpace(stage), MainStage) {
		return ""
	}
	prompt := strings.TrimSpace(session.ModeSystemPrompt)
	if prompt == "" {
		prompt = DefaultSystemPrompt
	} else if !strings.Contains(prompt, "Knowledge Base Capability") {
		prompt = corekbase.DefaultCapabilityPrompt + "\n\n" + prompt
	}
	if editing {
		prompt = strings.TrimSpace(prompt) + "\n\n" + DefaultEditingPrompt
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
		EditingMode:    session.EditingMode,
		WorkspaceDir:   workspaceDir,
		ChatDir:        chatDir,
		AvailableTools: toolNames,
		UserRequest:    req.Message,
	})
	values["kbase_source_root"] = strings.TrimSpace(session.KBaseSourceRoot)
	return agentcontract.RenderPromptTemplate(prompt, values)
}
