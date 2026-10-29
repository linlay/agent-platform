package runops

import (
	"context"
	"encoding/json"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/toolinput"
)

func (h *ToolHandler) WithMailbox(box *mailbox.Service) *ToolHandler { h.mailbox = box; return h }
func (h *ToolHandler) mailCaller(exec *contracts.ExecutionContext) (contracts.RunOrigin, *contracts.ToolExecutionResult) {
	if exec == nil || exec.RunControl == nil {
		return contracts.RunOrigin{}, mailError("run_context_required", "mail tools require an active root Run")
	}
	s := exec.Session
	owner := contracts.ResolveRunOwner(s.RunOwner)
	if s.SubTaskID != "" || s.AgentKey == "" || owner.AgentKey != s.AgentKey || (s.RunScopeID != "" && s.RunScopeID != s.ChatID) {
		return contracts.RunOrigin{}, mailError("run_caller_not_allowed", "mail tools are only available to a main Agent root Run")
	}
	if h.mailbox == nil {
		return contracts.RunOrigin{}, mailError("mail_unavailable", "mailbox is unavailable")
	}
	return contracts.RunOrigin{AgentKey: s.AgentKey, Subject: s.Subject, ChatID: s.ChatID, RunID: s.RunID, ToolID: exec.CurrentToolID}, nil
}
func mailError(code, message string) *contracts.ToolExecutionResult {
	r := errorResult(code, message)
	return &r
}
func mailResult(v any) contracts.ToolExecutionResult {
	b, _ := json.Marshal(v)
	var structured map[string]any
	_ = json.Unmarshal(b, &structured)
	return contracts.ToolExecutionResult{Output: string(b), Structured: structured}
}
func parseFiles(args map[string]any) ([]string, error) {
	v, ok := args["files"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var paths []string
	if v == nil || json.Unmarshal(b, &paths) != nil {
		return nil, &contracts.RunToolError{Code: "invalid_request", Message: "files must be an array of Chat file paths"}
	}
	return paths, nil
}
func (h *ToolHandler) mailRequest(args map[string]any, origin contracts.RunOrigin, exec *contracts.ExecutionContext) (mailbox.Request, error) {
	copy := contracts.CloneMap(args)
	toolinput.NormalizeBooleanFields(copy, map[string]string{"newChat": "b"})
	newChat := false
	if v, ok := copy["newChat"]; ok {
		var valid bool
		newChat, valid = v.(bool)
		if !valid {
			return mailbox.Request{}, &contracts.RunToolError{Code: "invalid_request", Message: "newChat must be a boolean"}
		}
		delete(copy, "newChat")
	}
	paths, err := parseFiles(copy)
	if err != nil {
		return mailbox.Request{}, err
	}
	delete(copy, "files")
	if _, ok := copy["chatId"]; ok && newChat {
		return mailbox.Request{}, &contracts.RunToolError{Code: "invalid_request", Message: "chatId and newChat are mutually exclusive"}
	}
	if !newChat {
		for _, key := range []string{"chatName", "modelKey", "reasoningEffort", "accessLevel"} {
			if _, ok := copy[key]; ok {
				return mailbox.Request{}, &contracts.RunToolError{Code: "invalid_request", Message: key + " requires newChat:true"}
			}
		}
	}
	r, err := startRequest(copy, origin, exec)
	if err != nil {
		return mailbox.Request{}, err
	}
	if !newChat {
		r.ModelKey = ""
		r.ReasoningEffort = ""
	}
	r.Input = contracts.CloneMap(args)
	r.ReferenceReadScope = exec.Session.ReferencesScope
	return mailbox.Request{Start: r, NewChat: newChat, Files: paths, Hop: exec.RunControl.MailHop(), UserChatReferences: exec.RunControl.UserChatReferences()}, nil
}
func (h *ToolHandler) invokeMail(ctx context.Context, tool string, args map[string]any, exec *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	origin, fail := h.mailCaller(exec)
	if fail != nil {
		return *fail, nil
	}
	switch tool {
	case "chat_send":
		req, err := h.mailRequest(args, origin, exec)
		if err != nil {
			return resultFromError(err), nil
		}
		key := startKey(origin)
		h.mu.Lock()
		review, reviewed := h.reviews[key]
		h.mu.Unlock()
		if reviewed {
			req.Start.Review = &contracts.RunStartReview{ParentAccessLevel: review.parentAccessLevel, ParentAccessVersion: review.parentAccessVersion, ApprovalDigest: review.approvalDigest, Consume: func(d string) bool {
				return contracts.ConsumeToolApproval(exec, contracts.ToolApprovalFingerprint(exec, "chat_send", "send", d))
			}}
		}
		r, err := h.mailbox.Send(ctx, req)
		if err != nil {
			return resultFromError(err), nil
		}
		return mailResult(r), nil
	case "chat_notify":
		if err := toolinput.Validate(args, map[string]string{"message": "s!", "files": "a"}, ""); err != nil {
			return resultFromError(err), nil
		}
		message := strings.TrimSpace(contracts.AnyStringNode(args["message"]))
		if message == "" {
			return errorResult("invalid_request", "message must not be empty"), nil
		}
		paths, err := parseFiles(args)
		if err != nil {
			return resultFromError(err), nil
		}
		r, err := h.mailbox.Notify(ctx, message, paths, origin, exec.RunControl.MailHop())
		if err != nil {
			return resultFromError(err), nil
		}
		return mailResult(r), nil
	case "chat_submit":
		fields := contracts.CloneMap(args)
		delete(fields, "params")
		if err := toolinput.Validate(fields, map[string]string{"runId": "s!", "awaitingId": "s!", "param": "o"}, ""); err != nil {
			return resultFromError(err), nil
		}
		b, err := json.Marshal(args)
		if err != nil {
			return resultFromError(err), nil
		}
		var req queryinput.SubmitRequest
		if err = json.Unmarshal(b, &req); err != nil {
			return resultFromError(err), nil
		}
		r, err := h.mailbox.Submit(ctx, req, exec)
		if err != nil {
			return resultFromError(err), nil
		}
		return mailResult(r), nil
	}
	return errorResult("invalid_tool", "unknown mail tool"), nil
}
func (h *ToolHandler) prepareMailApproval(ctx context.Context, args map[string]any, exec *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	origin, fail := h.mailCaller(exec)
	if fail != nil {
		return nil, nil
	}
	req, err := h.mailRequest(args, origin, exec)
	if err != nil {
		return nil, nil
	}
	key := startKey(origin)
	h.mu.Lock()
	old, exists := h.reviews[key]
	h.mu.Unlock()
	if exists {
		a := old.approval
		return &a, nil
	}
	p, err := h.mailbox.Prepare(ctx, req)
	if err != nil || !p.Plan.RequiresApproval {
		return nil, nil
	}
	plan := p.Plan
	a := contracts.ToolApproval{Fingerprint: contracts.ToolApprovalFingerprint(exec, "chat_send", "send", plan.ApprovalDigest), Title: "chat_send / " + plan.AccessLevel, Form: map[string]any{
		"action": "send", "caller": map[string]any{"agentKey": origin.AgentKey, "chatId": origin.ChatID, "runId": origin.RunID}, "target": map[string]any{"type": "agent", "key": p.Request.AgentKey, "name": plan.TargetName},
		"chatId": p.ChatID, "continuesChat": !p.OpenedChat, "chatName": req.Start.ChatName, "message": req.Start.Message, "references": req.Start.References, "files": req.Files, "parentAccessLevel": plan.ParentAccessLevel, "accessLevel": plan.AccessLevel, "modelKey": req.Start.ModelKey, "reasoningEffort": req.Start.ReasoningEffort}}
	h.mu.Lock()
	if existing, ok := h.reviews[key]; ok {
		a = existing.approval
	} else {
		h.reviews[key] = startReview{parentAccessLevel: plan.ParentAccessLevel, parentAccessVersion: plan.ParentAccessVersion, approvalDigest: plan.ApprovalDigest, approval: a}
	}
	h.mu.Unlock()
	return &a, nil
}
func (h *ToolHandler) mailStatus(args map[string]any, origin contracts.RunOrigin) (contracts.ToolExecutionResult, error) {
	if err := toolinput.Validate(args, map[string]string{"mailId": "s!"}, ""); err != nil {
		return resultFromError(err), nil
	}
	if h.mailbox == nil {
		return errorResult("mail_unavailable", "mailbox unavailable"), nil
	}
	m, err := h.mailbox.Owned(contracts.AnyStringNode(args["mailId"]), origin.AgentKey, origin.Subject)
	if err != nil {
		return resultFromError(err), nil
	}
	result := map[string]any{"mailId": m.ID, "status": m.Status, "delivery": m.Delivery, "chatId": m.ToChatID, "evidence": m.Evidence, "error": m.Error, "answeredBy": m.AnsweredBy}
	if m.Delivery == "started" && m.RunID != "" {
		if run, err := h.service.GetRunStatus(m.RunID); err == nil {
			result["run"] = run
		}
	}
	return mailResult(result), nil
}
