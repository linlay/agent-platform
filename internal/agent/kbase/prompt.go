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

const DefaultFileWorkspacePrompt = `KBASE File Workspace

Directories:
- Knowledge workspace: {{workspace_dir}}
- Current chat directory: {{chat_dir}}

Rules:
- The configured Workspace is the knowledge root for every dedicated KBASE run. Relative file-tool paths resolve inside this workspace.
- The structured file tools are always available for common text files.
- The Workspace is read-only unless this run explicitly enables editingMode.
- Store conversation artifacts and temporary files under the explicit current chat directory path. Chat files are not Workspace content.
- Reads and writes outside the Workspace and current chat directory follow AccessPolicy and may require user approval.
- Do not use shell commands or other tools to bypass the dedicated KBASE tool boundary.`

const DefaultEditingPrompt = `KBASE Editing Mode
The user explicitly enabled KBASE Workspace mutation for this run.

Rules:
- Make only changes required by the user's request. Do not perform opportunistic cleanup.
- Read an existing Workspace file with file_read before changing it. Prefer file_edit for localized changes and file_write only for a complete replacement or a new file.
- Workspace indexing is asynchronous and owned by the KBASE directory watcher. A successful file mutation does not mean the change is immediately searchable.
- Do not call kbase_refresh merely because a write result has no index status. Use it only when the user explicitly requests a refresh or when recovering from an indexing failure.
- In the final answer, list changed paths by Workspace, current chat directory, or other approved location, and summarize each path's lineStats.`

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
	prompt = strings.TrimSpace(prompt) + "\n\n" + DefaultFileWorkspacePrompt
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
		session.RuntimeContext.LocalPaths.ChatDir,
		session.RuntimeContext.SandboxPaths.ChatDir,
	)
	if session.AgentHasRuntimeSandbox {
		workspaceDir = agentcontract.FirstNonBlank(session.RuntimeContext.SandboxPaths.WorkspaceDir, workspaceDir)
		chatDir = agentcontract.FirstNonBlank(session.RuntimeContext.SandboxPaths.ChatDir, chatDir)
	}
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
	return agentcontract.RenderPromptTemplate(prompt, values)
}
