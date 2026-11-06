package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/plantasks"
	"agent-platform/internal/stream"
	sdk "github.com/coder/acp-go-sdk"
)

type observedTool struct {
	id, name, label   string
	translations      map[string]any
	input, output     json.RawMessage
	content           []json.RawMessage
	started, finished bool
	terminalOutput    strings.Builder
	outputChunks      int
	exitCode          *int
}
type turnEvents struct {
	content       *stream.MarkdownDestinationGuard
	chatID, runID string
	chatsDir      string
	tools         map[string]*observedTool
	emit          func(contracts.AgentDelta)
}

func (e *turnEvents) update(raw json.RawMessage) error {
	var message struct {
		Update struct {
			Type                string          `json:"sessionUpdate"`
			Content             json.RawMessage `json:"content"`
			ToolCallID          string          `json:"toolCallId"`
			Title, Kind, Status string
			RawInput, RawOutput json.RawMessage
			Meta                struct {
				TerminalOutputDelta *struct {
					TerminalID string `json:"terminal_id"`
					Data       string
				} `json:"terminal_output_delta"`
				TerminalExit *struct {
					TerminalID string `json:"terminal_id"`
					ExitCode   *int   `json:"exit_code"`
				} `json:"terminal_exit"`
			} `json:"_meta"`
			Entries json.RawMessage
		} `json:"update"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return agentengine.Error("rpc_connection_lost")
	}
	u := message.Update
	if u.Type != "agent_message_chunk" {
		e.flushContent()
	}
	switch u.Type {
	case "agent_message_chunk", "agent_thought_chunk":
		var c struct{ Type, Text string }
		if json.Unmarshal(u.Content, &c) != nil || c.Type != "text" {
			return nil
		}
		if u.Type == "agent_message_chunk" {
			text := c.Text
			if e.content != nil {
				text = e.content.Write(text)
			}
			if text != "" {
				e.emit(contracts.DeltaContent{Text: text})
			}
		} else {
			e.emit(contracts.DeltaReasoning{Text: c.Text})
		}
	case "tool_call", "tool_call_update":
		if u.ToolCallID == "" {
			return agentengine.Error("rpc_connection_lost")
		}
		tool := e.tools[u.ToolCallID]
		if tool == nil {
			if len(e.tools) >= 256 {
				return agentengine.Error("rpc_connection_lost")
			}
			label, translations := toolTitle(u.Title, u.Kind)
			name := "acp/other"
			if u.Kind != "" {
				name = "acp/" + u.Kind
			}
			tool = &observedTool{id: u.ToolCallID, name: name, label: label, translations: translations}
			e.tools[u.ToolCallID] = tool
		}
		if tool.finished {
			return nil
		}
		if u.Title != "" {
			tool.label = u.Title
			tool.translations = nil
		}
		if len(u.RawInput) > 0 {
			tool.input = u.RawInput
		}
		if len(u.RawOutput) > 0 {
			tool.output = u.RawOutput
		}
		if len(u.Content) > 0 && string(u.Content) != "null" {
			if json.Unmarshal(u.Content, &tool.content) != nil {
				return agentengine.Error("rpc_connection_lost")
			}
		}
		if !tool.started {
			args := `{}`
			if len(tool.input) > 0 {
				args = string(tool.input)
			}
			e.emit(contracts.DeltaToolCall{ID: tool.id, Name: tool.name, ArgsDelta: args, ToolLabel: tool.label, ToolI18n: tool.translations})
			tool.started = true
		}
		// Codex's standard renderer sends merged terminal output through this
		// extension even without host terminal execution capability.
		if chunk := u.Meta.TerminalOutputDelta; chunk != nil && chunk.TerminalID == tool.id && chunk.Data != "" {
			tool.terminalOutput.WriteString(chunk.Data)
			e.emit(contracts.DeltaToolOutput{ToolID: tool.id, ToolName: tool.name, Stream: "stdout", Delta: chunk.Data, ChunkIndex: tool.outputChunks})
			tool.outputChunks++
		}
		if exit := u.Meta.TerminalExit; exit != nil && exit.TerminalID == tool.id {
			tool.exitCode = exit.ExitCode
		}
		if u.Status == "completed" || u.Status == "failed" {
			e.finishTool(tool, u.Status == "failed")
		}
	case "plan":
		var entries []struct{ Content, Status string }
		if json.Unmarshal(u.Entries, &entries) != nil || len(entries) > 256 {
			return agentengine.Error("rpc_connection_lost")
		}
		state := &contracts.PlanRuntimeState{PlanID: e.runID + "-acp-plan", Tasks: []contracts.PlanTask{}}
		for index, entry := range entries {
			status := entry.Status
			if status == "pending" {
				status = "init"
			}
			status = plantasks.NormalizePlanTaskStatus(status)
			if status == "" {
				return agentengine.Error("capability_unsupported")
			}
			state.Tasks = append(state.Tasks, contracts.PlanTask{TaskID: fmt.Sprintf("%s-%d", state.PlanID, index), Description: entry.Content, Status: status})
		}
		if e.chatsDir != "" {
			if _, err := plantasks.PersistState(e.chatsDir, e.chatID, e.runID, state); err != nil {
				return err
			}
		}
		e.emit(contracts.DeltaPlanUpdate{ChatID: e.chatID, PlanID: state.PlanID, Plan: plantasks.PlanTasksArray(state)})
	}
	return nil
}

func (e *turnEvents) finishTool(tool *observedTool, failed bool) {
	changes := map[string]map[string]any{}
	for _, raw := range tool.content {
		var diff struct {
			Type, Path, NewText string
			OldText             *string
		}
		if json.Unmarshal(raw, &diff) == nil && diff.Type == "diff" {
			operation := "edit"
			if diff.OldText == nil {
				operation = "create"
			}
			before := ""
			if diff.OldText != nil {
				before = *diff.OldText
			}
			changes[tool.id] = map[string]any{"filePath": diff.Path, "operation": operation, "lineStats": filetools.LineStatsPayload(filetools.ComputeLineDiffStats(before, diff.NewText))}
			break
		}
	}
	e.emit(contracts.DeltaToolEnd{ToolIDs: []string{tool.id}, FileChanges: changes})
	content := tool.content
	if tool.terminalOutput.Len() > 0 {
		text, _ := json.Marshal(map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": tool.terminalOutput.String()}})
		content = append(content, text)
	}
	result := contracts.ToolExecutionResult{Structured: map[string]any{"content": content}}
	if tool.exitCode != nil {
		result.ExitCode = *tool.exitCode
		result.Structured["exitCode"] = *tool.exitCode
	}
	if len(tool.input) > 0 {
		result.Structured["input"] = tool.input
	}
	if len(tool.output) > 0 {
		result.Structured["output"] = tool.output
	}
	if failed {
		result.Error = "External tool did not complete successfully"
		// The ordinary failure pipeline reads Output, not Structured.
		result.Output = contracts.MarshalJSON(result.Structured)
	}
	e.emit(contracts.DeltaToolResult{ToolID: tool.id, ToolName: tool.name, ToolLabel: tool.label, ToolI18n: tool.translations, Result: result})
	tool.finished = true
}
func toolTitle(title, kind string) (string, map[string]any) {
	if title != "" {
		return title, nil
	}
	en, zh := "Tool call", "工具调用"
	switch kind {
	case "read":
		en, zh = "Read file", "读取文件"
	case "edit":
		en, zh = "Edit file", "编辑文件"
	case "delete":
		en, zh = "Delete file", "删除文件"
	case "move":
		en, zh = "Move file", "移动文件"
	case "search":
		en, zh = "Search", "搜索"
	case "execute":
		en, zh = "Run command", "执行命令"
	case "fetch":
		en, zh = "Fetch", "获取内容"
	case "think":
		en, zh = "Think", "思考"
	}
	return en, map[string]any{"en": map[string]any{"label": en}, "zh-CN": map[string]any{"label": zh}}
}
func (e *turnEvents) flushContent() {
	if e.content != nil {
		if text := e.content.Flush(); text != "" {
			e.emit(contracts.DeltaContent{Text: text})
		}
	}
}

func (e *turnEvents) finish() {
	e.flushContent()
	for _, tool := range e.tools {
		if !tool.finished {
			e.finishTool(tool, true)
		}
	}
}

// Error payloads contain stable host codes, never raw adapter error text.
func callFailure(err error) contracts.DeltaError {
	code := agentengine.Error("rpc_connection_lost")
	state := "unknown"
	errors.As(err, &code)
	if errors.Is(err, ErrBusy) {
		code = "engine_busy"
	}
	var rpc *sdk.RequestError
	if errors.As(err, &rpc) && rpc.Code == -32000 {
		code = "auth_required"
	}
	var call *CallError
	if errors.As(err, &call) {
		state = call.ExecutionState
	}
	return contracts.DeltaError{Error: map[string]any{"code": string(code), "message": string(code), "executionState": state}}
}

// ACP runs in a known Workspace. Qualify only existing local files; leave
// network URLs, explicit references, code and unavailable targets untouched.
func workspaceDestination(workspace, destination string) string {
	if workspace == "" {
		return destination
	}
	target := strings.TrimSpace(destination)
	start, end := 0, len(target)
	if strings.HasPrefix(target, "<") {
		start, end = 1, strings.Index(target, ">")
		if end < start {
			return destination
		}
	} else if i := strings.IndexAny(target, " \t\r\n"); i >= 0 {
		end = i
	}
	name := target[start:end]
	if name == "" || len(name) > 2048 || strings.HasPrefix(name, "@") || strings.ContainsAny(name, ":?#\\\x00\r\n") || !filepath.IsLocal(name) {
		return destination
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return destination
		}
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return destination
	}
	file, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return destination
	}
	relative, err := filepath.Rel(root, file)
	if err != nil || !filepath.IsLocal(relative) {
		return destination
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return destination
	}
	return target[:start] + "@workspace/" + filepath.ToSlash(filepath.Clean(name)) + target[end:]
}
