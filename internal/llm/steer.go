package llm

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
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
		text := req.Message
		if strings.TrimSpace(text) == "" && len(refs) == 0 {
			// Same rule as a blank query: the public message stays empty.
			text = querymessages.EmptyQueryContinuation
		}
		req.PreparedMessages = []map[string]any{{"role": "user", "content": querymessages.BuildContentWithImageBlocks(text, refs, blocks, inputOptions)}}
		return req, nil
	}
}

// materializeHistorySteer resolves reference-only history at the model boundary.
// Legacy snapshots have no marker and retain their original content. Read failures
// are represented in the user input instead of making the whole chat unusable.
func (e *LLMAgentEngine) materializeHistorySteer(raw map[string]any, session contracts.QuerySession, vision bool) map[string]any {
	input, ok := raw[chat.SteerHistoryInputKey]
	if !ok {
		return raw
	}
	encoded, err := json.Marshal(input)
	var steer api.SteerRequest
	if err != nil || json.Unmarshal(encoded, &steer) != nil {
		return raw
	}
	chatID := steer.ChatID
	if chatID == "" {
		chatID = session.ChatID
	}
	chatDir := filepath.Join(e.cfg.Paths.ChatsDir, chatID)
	if chatID == session.ChatID && session.ChatRoot != "" {
		chatDir = session.ChatRoot
	}
	container := !e.cfg.IsLocalMode() && session.AgentHasRuntimeSandbox
	var refs []api.Reference
	var blocks []map[string]any
	var unavailable []string
	for _, ref := range steer.References {
		var prepared []api.Reference
		var images []map[string]any
		var prepareErr error
		if !chat.ValidChatID(chatID) {
			prepareErr = fmt.Errorf("invalid chat identity")
		} else {
			prepared, images, prepareErr = chatresource.PrepareSteerReferences(chatID, chatDir, container, []api.Reference{ref})
		}
		if prepareErr != nil {
			// Never retain an unvalidated historical host path as a tool hint.
			label := ref.URL
			if label == "" {
				label = ref.Name
			}
			if label == "" {
				label = ref.ID
			}
			unavailable = append(unavailable, fmt.Sprintf("附件已不可用：%q", label))
			continue
		}
		refs = append(refs, prepared...)
		if vision {
			blocks = append(blocks, images...)
		}
	}
	text := steer.Message
	if len(unavailable) > 0 {
		text += "\n\n" + strings.Join(unavailable, "\n")
	}
	options := querymessages.BuildOptions{
		AdvancedUserPrompt: session.AdvancedUserPrompt, WorkspaceDir: session.WorkspaceRoot,
		ChatDir: chatDir, RunID: steer.RunID, RequestID: steer.RequestID,
		AgentKey: session.AgentKey, TeamID: session.TeamID, Role: "user",
	}
	// Preserve the historical event time rather than inventing a new input time.
	switch ts := raw["ts"].(type) {
	case int64:
		options.Now = time.UnixMilli(ts)
	case float64:
		options.Now = time.UnixMilli(int64(ts))
	}
	result := make(map[string]any, len(raw))
	for key, value := range raw {
		if key != chat.SteerHistoryInputKey {
			result[key] = value
		}
	}
	result["content"] = querymessages.BuildContentWithImageBlocks(text, refs, blocks, options)
	return result
}

func (e *LLMAgentEngine) BindSteerPreparer(session contracts.QuerySession, control *contracts.RunControl) error {
	model, err := e.models.GetModel(session.ModelKey)
	if err != nil {
		return err
	}
	control.SetSteerPreparer(e.steerPreparer(session, model.IsVision))
	return nil
}
