package runops

import (
	"agent-platform/internal/contracts/queryinput"
	"context"
	"testing"
)

func TestChatStartOptionalArgumentsAndStrictValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		code  string
	}{
		{"omitted", nil, ""},
		{"explicit default", map[string]any{"accessLevel": "default"}, ""},
		{"all optional", map[string]any{"accessLevel": "auto_approve", "chatName": "测试", "references": []any{map[string]any{"type": "skill", "id": "demo"}}}, ""},
		{"empty skills", map[string]any{"references": []any{}}, ""},
		{"model", map[string]any{"modelKey": " fast ", "reasoningEffort": "high"}, ""},
		{"effort only", map[string]any{"reasoningEffort": "NONE"}, ""},
		{"wrong effort", map[string]any{"reasoningEffort": "extreme"}, "invalid_request"},
		{"empty agent", map[string]any{"agentKey": ""}, "invalid_request"},
		{"blank agent", map[string]any{"agentKey": " "}, "invalid_request"},
		{"null agent", map[string]any{"agentKey": nil}, "invalid_request"},
		{"wrong agent type", map[string]any{"agentKey": 1}, "invalid_request"},
		{"empty model", map[string]any{"modelKey": " "}, "invalid_request"},
		{"wrong model type", map[string]any{"modelKey": 1}, "invalid_request"},
		{"provider model ID", map[string]any{"modelId": "gpt"}, "unknown_argument"},
		{"unknown", map[string]any{"taskName": "old"}, "unknown_argument"},
		{"null", map[string]any{"accessLevel": nil}, "invalid_request"},
		{"wrong level type", map[string]any{"accessLevel": true}, "invalid_request"},
		{"wrong level", map[string]any{"accessLevel": "auto"}, "invalid_request"},
		{"empty level", map[string]any{"accessLevel": " "}, "invalid_request"},
		{"wrong message", map[string]any{"message": 42}, "invalid_request"},
		{"wrong skills", map[string]any{"references": "demo"}, "invalid_request"},
		{"empty skill ID", map[string]any{"references": []any{map[string]any{"type": "skill", "id": " "}}}, "invalid_request"},
		{"null skills", map[string]any{"references": nil}, "invalid_request"},
		{"empty name", map[string]any{"chatName": " "}, "invalid_request"},
		{"name with continuation", map[string]any{"chatName": "test", "chatId": "existing"}, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newFakeRunToolService()
			handler := NewToolHandler(service, nil)
			args := map[string]any{"agentKey": "target", "message": "hello"}
			for key, value := range tc.extra {
				args[key] = value
			}
			result, err := handler.Invoke(context.Background(), StartToolName, args, runToolExecContext("alice", "tool"))
			if err != nil || result.Error != tc.code {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if tc.code != "" && service.starts != 0 {
				t.Fatal("invalid input started a run")
			}
			if tc.code == "" {
				if tc.name == "all optional" {
					req := service.requests[0]
					if req.AccessLevel != "auto_approve" || req.ChatName != "测试" || len(queryinput.ResourceIDs(req.References, "skill")) != 1 || queryinput.ResourceIDs(req.References, "skill")[0] != "demo" {
						t.Fatalf("options not forwarded: %#v", req)
					}
				}
				if tc.name == "model" {
					if req := service.requests[0]; req.ModelKey != "fast" || req.ReasoningEffort != "HIGH" {
						t.Fatalf("model options not forwarded: %#v", req)
					}
				}
				result, err = handler.Invoke(context.Background(), StartToolName, args, runToolExecContext("alice", "tool"))
				if err != nil || result.Error != "" || service.starts != 1 {
					t.Fatalf("retry not idempotent: %#v %v starts=%d", result, err, service.starts)
				}
			}
		})
	}
}

func _(t *testing.T) {
	for _, args := range []map[string]any{
		{"message": "hello"},
		{"agentKey": "worker", "message": "hello"},
		{"message": "hello", "modelKey": "fast"},
		{"message": "hello"},
		{"message": "hello"},
	} {
		service := newFakeRunToolService()
		handler := NewToolHandler(service, nil)
		exec := runToolExecContext("alice", "tool")
		approval, err := handler.PrepareToolApproval(context.Background(), StartToolName, args, exec)
		if err != nil || approval != nil {
			t.Fatalf("removed argument requested approval: %#v %v", approval, err)
		}
		result, err := handler.Invoke(context.Background(), StartToolName, args, exec)
		if err != nil || result.Error != "unknown_argument" || service.starts != 0 {
			t.Fatalf("args=%#v result=%#v err=%v starts=%d", args, result, err, service.starts)
		}
	}
}

func TestChatStartModelOptionsReviewAndIdempotency(t *testing.T) {
	for _, field := range []string{"modelKey", "reasoningEffort"} {
		t.Run(field, func(t *testing.T) {
			service := newFakeRunToolService()
			service.parentLevel = "default"
			handler := NewToolHandler(service, nil)
			args := map[string]any{"agentKey": "worker", "message": "task", "modelKey": "fast", "reasoningEffort": "HIGH", "accessLevel": "full_access"}
			exec := runToolExecContext("alice", "review")
			approval, err := handler.PrepareToolApproval(context.Background(), StartToolName, args, exec)
			if err != nil || approval == nil || approval.Form["modelKey"] != "fast" || approval.Form["reasoningEffort"] != "HIGH" {
				t.Fatalf("approval=%#v err=%v", approval, err)
			}
			// A separate invocation with no elevation can be replayed only with the same model choices.
			delete(args, "accessLevel")
			exec = runToolExecContext("alice", "start")
			result, err := handler.Invoke(context.Background(), StartToolName, args, exec)
			if err != nil || result.Error != "" {
				t.Fatalf("%#v %v", result, err)
			}
			if field == "modelKey" {
				args[field] = "other"
			} else {
				args[field] = "LOW"
			}
			result, err = handler.Invoke(context.Background(), StartToolName, args, exec)
			if err != nil || result.Error != "idempotency_conflict" || service.starts != 1 {
				t.Fatalf("%#v %v starts=%d", result, err, service.starts)
			}
		})
	}
}

func TestChatStartDefaultsTargetBeforeReviewAndIdempotency(t *testing.T) {
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	exec := runToolExecContext("alice", "default-agent")
	args := map[string]any{"chatName": "冒烟-A1-创建编程智能体", "message": "创建编程智能体", "accessLevel": "full_access"}
	approval, err := handler.PrepareToolApproval(context.Background(), StartToolName, args, exec)
	if err != nil || approval == nil {
		t.Fatalf("approval=%#v err=%v", approval, err)
	}
	target := approval.Form["target"].(map[string]any)
	if target["key"] != "zenmi" || target["type"] != "agent" || service.starts != 0 {
		t.Fatalf("wrong review target or premature start: %#v starts=%d", target, service.starts)
	}
	exec.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	result, err := handler.Invoke(context.Background(), StartToolName, args, exec)
	if err != nil || result.Error != "" || service.starts != 1 {
		t.Fatalf("result=%#v err=%v starts=%d", result, err, service.starts)
	}
	req := service.requests[0]
	if req.AgentKey != "zenmi" || req.ChatID != "" || req.ChatName != args["chatName"] || req.Review == nil {
		t.Fatalf("wrong effective request: %#v", req)
	}
	if !req.Review.Consume(req.Review.ApprovalDigest) || req.Review.Consume(req.Review.ApprovalDigest) {
		t.Fatal("default target approval must be consumable exactly once")
	}
	args["agentKey"] = "zenmi"
	result, err = handler.Invoke(context.Background(), StartToolName, args, exec)
	if err != nil || result.Error != "" || service.starts != 1 {
		t.Fatalf("explicit current Agent replay was not idempotent: %#v %v starts=%d", result, err, service.starts)
	}
	args["agentKey"] = "another"
	result, err = handler.Invoke(context.Background(), StartToolName, args, exec)
	if err != nil || result.Error != "idempotency_conflict" || service.starts != 1 {
		t.Fatalf("changed target was accepted: %#v %v starts=%d", result, err, service.starts)
	}
}

func TestChatStartInheritsCurrentRunModelBeforeReviewAndStart(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		args                 map[string]any
		agent, model, effort string
	}{
		{"message only", map[string]any{"message": "task"}, "zenmi", "run-model", "HIGH"},
		{"other agent", map[string]any{"message": "task", "agentKey": "worker"}, "worker", "run-model", "HIGH"},
		{"continue", map[string]any{"message": "task", "chatId": "existing"}, "zenmi", "run-model", "HIGH"},
		{"model only", map[string]any{"message": "task", "modelKey": "override"}, "zenmi", "override", "HIGH"},
		{"effort only", map[string]any{"message": "task", "reasoningEffort": "LOW"}, "zenmi", "run-model", "LOW"},
		{"disable", map[string]any{"message": "task", "reasoningEffort": "NONE"}, "zenmi", "run-model", "NONE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newFakeRunToolService()
			handler := NewToolHandler(service, nil)
			exec := runToolExecContext("alice", "inherit")
			exec.Session.ModelKey = "agent-default"
			exec.CurrentModel = &queryinput.QueryModelOptions{Key: "run-model", ReasoningEffort: "HIGH"}
			result, err := handler.Invoke(context.Background(), StartToolName, tc.args, exec)
			if err != nil || result.Error != "" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			req := service.requests[0]
			if req.AgentKey != tc.agent || req.ModelKey != tc.model || req.ReasoningEffort != tc.effort {
				t.Fatalf("effective request=%#v", req)
			}
			// Effective inherited values participate in idempotency.
			exec.CurrentModel.Key = "changed"
			exec.CurrentModel.ReasoningEffort = "MAX"
			result, err = handler.Invoke(context.Background(), StartToolName, tc.args, exec)
			if err != nil || result.Error != "idempotency_conflict" || service.starts != 1 {
				t.Fatalf("replay=%#v err=%v starts=%d", result, err, service.starts)
			}
		})
	}
	service := newFakeRunToolService()
	handler := NewToolHandler(service, nil)
	exec := runToolExecContext("alice", "inherit-review")
	exec.CurrentModel = &queryinput.QueryModelOptions{Key: "run-model", ReasoningEffort: "NONE"}
	args := map[string]any{"message": "task", "accessLevel": "full_access"}
	approval, err := handler.PrepareToolApproval(context.Background(), StartToolName, args, exec)
	if err != nil || approval == nil || approval.Form["modelKey"] != "run-model" || approval.Form["reasoningEffort"] != "NONE" {
		t.Fatalf("approval=%#v err=%v", approval, err)
	}
	exec.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	result, err := handler.Invoke(context.Background(), StartToolName, args, exec)
	if err != nil || result.Error != "" || service.starts != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if req := service.requests[0]; req.ModelKey != "run-model" || req.ReasoningEffort != "NONE" || req.Review == nil {
		t.Fatalf("reviewed request=%#v", req)
	}
}
