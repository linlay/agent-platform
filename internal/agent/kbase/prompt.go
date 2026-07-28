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

Directories:
- Knowledge source: {{kbase_source_root}}
- Current chatspace: {{chat_dir}}

Rules:
- Knowledge-source mutations only support UTF-8 Markdown files ending in .md. Never modify .markdown files, other source file types, directories, or paths that escape through symlinks.
- Use the current chatspace for temporary text files, intermediate results, and conversation deliverables. Chatspace files are not knowledge-source content.
- Reads outside the knowledge source and current chatspace follow AccessPolicy and may require user approval. Writes outside both roots are blocked.
- Make only changes required by the user's request. Do not perform opportunistic cleanup.
- Read an existing knowledge-source file with file_read before changing it. Prefer file_edit for localized changes and file_write only for a complete replacement or a new file.
- file_glob and file_grep are restricted to .md files when searching the knowledge source, and use normal text-file behavior in the current chatspace or an approved external read root.
- Do not use shell commands or other tools to bypass the editing boundary.
- A successful knowledge-source mutation includes a kbase-index hook result. A chatspace mutation does not trigger kbase-index.
- Only claim that a knowledge-source change is searchable when its kbase-index hook reports success.
- If the file was saved but indexing failed or was skipped, report that distinction clearly and use kbase_refresh at most once to retry a failed index update.
- In the final answer, separately list knowledge-source changes and chatspace artifacts, and summarize each changed path's lineStats.`

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
