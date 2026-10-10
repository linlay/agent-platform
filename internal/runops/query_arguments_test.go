package runops

import (
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
		{"all optional", map[string]any{"accessLevel": "auto_approve", "chatName": "测试", "mustUseSkills": []any{"demo"}}, ""},
		{"empty skills", map[string]any{"mustUseSkills": []any{}}, ""},
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
		{"wrong skills", map[string]any{"mustUseSkills": "demo"}, "invalid_request"},
		{"empty skill ID", map[string]any{"mustUseSkills": []any{" "}}, "invalid_request"},
		{"null skills", map[string]any{"mustUseSkills": nil}, "invalid_request"},
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
					if req.AccessLevel != "auto_approve" || req.ChatName != "测试" || len(req.MustUseSkills) != 1 || req.MustUseSkills[0] != "demo" {
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

func TestChatStartRejectsTeamID(t *testing.T) {
	for _, args := range []map[string]any{
		{"teamId": "research", "message": "hello"},
		{"teamId": "research", "agentKey": "worker", "message": "hello"},
		{"teamId": "research", "message": "hello", "modelKey": "fast"},
		{"teamId": "", "message": "hello"},
		{"teamId": nil, "message": "hello"},
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
