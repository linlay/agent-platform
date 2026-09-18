package llm

import (
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/contracts"
	"agent-platform/internal/querymessages"
)

func (e *LLMAgentEngine) steerPreparer(session contracts.QuerySession, vision bool) func(api.SteerRequest) (api.SteerRequest, error) {
	chatDir := session.ChatRoot
	if chatDir == "" {
		chatDir = filepath.Join(e.cfg.Paths.ChatsDir, session.ChatID)
	}
	container := !e.cfg.IsLocalMode() && session.AgentHasRuntimeSandbox
	options := querymessages.BuildOptions{
		AdvancedUserPrompt: session.AdvancedUserPrompt, WorkspaceDir: session.WorkspaceRoot,
		ChatDir: chatDir, RunID: session.RunID, AgentKey: session.AgentKey, TeamID: session.TeamID, Role: "user",
	}
	chatID, runID := session.ChatID, session.RunID
	return func(req api.SteerRequest) (api.SteerRequest, error) {
		if req.RunID != runID || (strings.TrimSpace(req.ChatID) != "" && req.ChatID != chatID) {
			return req, fmt.Errorf("steer does not match the active run/chat")
		}
		refs, blocks, err := chatresource.PrepareSteerReferences(chatID, chatDir, container, req.References)
		if err != nil {
			return req, err
		}
		// Non-vision models can inspect images through their configured tools.
		// Keep validated file references, but do not send unsupported image blocks.
		if !vision {
			blocks = nil
		}
		req.ChatID, req.References = chatID, refs
		inputOptions := options
		inputOptions.RequestID = req.RequestID
		req.PreparedMessages = []map[string]any{{"role": "user", "content": querymessages.BuildContentWithImageBlocks(req.Message, refs, blocks, inputOptions)}}
		return req, nil
	}
}
