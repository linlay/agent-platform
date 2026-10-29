package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/resultsubmit"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

// executeFileKey marks an input value that names a file part of the request:
// {"$file": "<part>"}.
const executeFileKey = "$file"

// Execute runs one /api/execute call in a new Chat and returns the Run's
// accepted result_submit. The caller waits for the whole Run; when it goes
// away (ctx done) the Run is interrupted, since nobody can receive the result.
func (s *Service) Execute(ctx context.Context, cmd runtimetypes.ExecuteCommand) (runtimetypes.ExecuteResult, error) {
	agentKey := strings.TrimSpace(cmd.AgentKey)
	if agentKey == "" {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", "agentKey is required", "")
	}
	if strings.TrimSpace(cmd.Message) == "" {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", "message is required", "")
	}
	if _, err := resultsubmit.CompileOutputSchema(cmd.OutputSchema); err != nil {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", err.Error(), "")
	}
	input, fileRefs, err := decodeExecuteInput(cmd.Input)
	if err != nil {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", err.Error(), "")
	}
	if err := matchExecuteFiles(fileRefs, cmd.FileParts); err != nil {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", err.Error(), "")
	}
	if _, ok := contracts.NormalizeRequestedAccessLevel(cmd.AccessLevel); !ok {
		return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", "accessLevel must be default, ask_for_approval, auto_approve or full_access", "")
	}
	if err := s.checkExecuteTarget(agentKey); err != nil {
		return runtimetypes.ExecuteResult{}, err
	}

	chatID := newChatID()
	if len(cmd.FileParts) > 0 {
		if cmd.StoreFiles == nil {
			return runtimetypes.ExecuteResult{}, fmt.Errorf("execute input files have no store")
		}
		paths, err := cmd.StoreFiles(chatID)
		if err != nil {
			return runtimetypes.ExecuteResult{}, err
		}
		input = replaceExecuteFiles(input, paths)
	}
	taskInput := ""
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return runtimetypes.ExecuteResult{}, executeRequestError(400, "invalid_request", "input: "+err.Error(), "")
		}
		taskInput = string(encoded)
	}
	contract := &contracts.ResultContract{OutputSchema: cmd.OutputSchema, Input: taskInput}
	stream := false
	query := runtimetypes.QueryCommand{
		OriginalQuery:   cmd.OriginalQuery,
		RequestID:       strings.TrimSpace(cmd.RequestID),
		ChatID:          chatID,
		AgentKey:        agentKey,
		Message:         cmd.Message,
		AccessLevel:     cmd.AccessLevel,
		Stream:          &stream,
		ResultContract:  contract,
		Locale:          cmd.Locale,
		ResourceBaseURL: cmd.ResourceBaseURL,
		ChatSource:      cmd.ChatSource,
		Caller:          cmd.Caller,
	}
	ctx = queryContext(ctx, query)
	prepared, err := s.prepare(ctx, query)
	if err != nil {
		return runtimetypes.ExecuteResult{}, err
	}
	registered, statusErr := s.RegisterPreparedQuery(ctx, prepared)
	if statusErr != nil {
		releaseQuery(prepared.Release)
		return runtimetypes.ExecuteResult{}, statusErr
	}
	stop := context.AfterFunc(ctx, func() {
		s.deps.Runs.Interrupt(interruptRequestForQuery(prepared.Req, contracts.InterruptSourceHTTPAPI, contracts.InterruptReasonUserCancelled, "execute caller disconnected"))
	})
	defer stop()
	result, err := s.executePreparedLocalQuery(prepared, registered, nil, nil)
	if err != nil {
		return runtimetypes.ExecuteResult{}, err
	}
	if submission := contract.Submission(); submission != nil {
		return runtimetypes.ExecuteResult{ChatID: chatID, Status: submission.Status, Output: submission.Output, Reason: submission.Reason}, nil
	}
	return runtimetypes.ExecuteResult{}, executeRunError(chatID, result)
}

// checkExecuteTarget admits only a native Agent whose effective tools include
// result_submit; the mode does not matter.
func (s *Service) checkExecuteTarget(agentKey string) error {
	if s.deps.Registry == nil {
		return fmt.Errorf("agent registry is unavailable")
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return executeRequestError(404, "agent_not_found", "agent not found: "+agentKey, "")
	}
	if sessionbuild.IsProxyRoutedAgent(def) || catalog.AgentUsesACPCoderBackend(def) || sessionbuild.IsProxyAgentMode(def.Mode) {
		return executeRequestError(400, "execute_unsupported_agent", "only native Agents can be executed", "")
	}
	if !slices.Contains(def.Tools, contracts.ResultSubmitToolName) {
		return executeRequestError(400, "result_submit_unavailable", "agent "+agentKey+" has no result_submit tool; add it to the Agent's tools or use an EXECUTOR agent", "")
	}
	return nil
}

// configureResultContractSession gives the Run its result_submit with the
// caller's output format. Admission already checked the tool is mounted; the
// session check covers tools removed while the Run was being prepared.
func (s *Service) configureResultContractSession(session *contracts.QuerySession, contract *contracts.ResultContract) error {
	if session.PlanningMode {
		return executeRequestError(400, "invalid_request", "a Run started through /api/execute cannot plan", session.ChatID)
	}
	if !slices.Contains(session.ToolNames, contracts.ResultSubmitToolName) {
		return executeRequestError(400, "result_submit_unavailable", "agent "+session.AgentKey+" has no result_submit tool", session.ChatID)
	}
	if s.deps.Tools == nil {
		return fmt.Errorf("tool registry is unavailable")
	}
	var base *queryinput.ToolDefinition
	for _, definition := range s.deps.Tools.Definitions() {
		if contracts.IsResultSubmitToolName(definition.Name) {
			found := definition
			base = &found
			break
		}
	}
	if base == nil {
		return fmt.Errorf("embedded tool %q is unavailable", contracts.ResultSubmitToolName)
	}
	definition, err := resultsubmit.BuildToolDefinition(*base, contract.OutputSchema)
	if err != nil {
		return err
	}
	session.ModeToolDefinitions = append(session.ModeToolDefinitions, definition)
	session.ResultContract = contract
	return nil
}

// executeRunError maps a Run that ended without an accepted result.
func executeRunError(chatID string, result queryRunResult) error {
	payload := result.ErrorPayload
	if len(payload) == 0 && result.Completion != nil && result.FinishReason == "" {
		result.FinishReason = result.Completion.FinishReason
	}
	code := strings.TrimSpace(contracts.AnyStringNode(payload["code"]))
	message := strings.TrimSpace(contracts.AnyStringNode(payload["message"]))
	switch {
	case result.FinishReason == "cancel":
		return executeRequestError(409, "cancelled", "the Run was cancelled before it delivered a result", chatID)
	case code == string(apperrors.CodeApprovalRequired):
		return executeRequestError(403, code, message, chatID)
	case code == string(apperrors.CodeOutputMissing), code == string(apperrors.CodeOutputInvalid):
		return executeRequestError(422, code, message, chatID)
	case code != "" || result.FinishReason == "error":
		if message == "" {
			message = firstNonBlank(result.ErrorMessage, "the Run failed")
		}
		err := executeRequestError(500, "run_error", message, chatID)
		if data, ok := err.Data.(map[string]any); ok && code != "" {
			data["cause"] = contracts.CloneMap(payload)
		}
		return err
	default:
		return executeRequestError(422, string(apperrors.CodeOutputMissing), "the Run ended without calling result_submit", chatID)
	}
}

func executeRequestError(status int, code string, message string, chatID string) *statusError {
	data := map[string]any{"error": map[string]any{"code": code, "message": message}}
	if chatID != "" {
		data["chatId"] = chatID
	}
	return &statusError{Status: status, Code: code, Message: message, Data: data}
}

// decodeExecuteInput decodes the task data and collects the file parts it
// names. Numbers keep their JSON spelling.
func decodeExecuteInput(raw json.RawMessage) (any, []string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil {
		return nil, nil, fmt.Errorf("input must be JSON: %v", err)
	}
	refs := map[string]struct{}{}
	if err := collectExecuteFiles(input, refs); err != nil {
		return nil, nil, err
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return input, out, nil
}

func collectExecuteFiles(value any, refs map[string]struct{}) error {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[executeFileKey]; ok {
			part, isText := raw.(string)
			if len(typed) != 1 || !isText || strings.TrimSpace(part) == "" {
				return fmt.Errorf(`input file reference must be {"%s": "<part name>"}`, executeFileKey)
			}
			refs[part] = struct{}{}
			return nil
		}
		for _, child := range typed {
			if err := collectExecuteFiles(child, refs); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := collectExecuteFiles(child, refs); err != nil {
				return err
			}
		}
	}
	return nil
}

// matchExecuteFiles requires the input and the request's file parts to name
// the same files.
func matchExecuteFiles(refs []string, parts []string) error {
	provided := map[string]bool{}
	for _, part := range parts {
		provided[part] = true
	}
	for _, ref := range refs {
		if !provided[ref] {
			return fmt.Errorf("input names file part %q, which the request does not contain", ref)
		}
		delete(provided, ref)
	}
	if len(provided) > 0 {
		extra := make([]string, 0, len(provided))
		for part := range provided {
			extra = append(extra, part)
		}
		sort.Strings(extra)
		return fmt.Errorf("file part %q is not referenced by input", extra[0])
	}
	return nil
}

func replaceExecuteFiles(value any, paths map[string]string) any {
	switch typed := value.(type) {
	case map[string]any:
		if part, ok := typed[executeFileKey].(string); ok && len(typed) == 1 {
			return paths[part]
		}
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = replaceExecuteFiles(child, paths)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = replaceExecuteFiles(child, paths)
		}
		return out
	default:
		return value
	}
}
