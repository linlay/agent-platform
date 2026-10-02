package runops

import (
	"context"
	"testing"
)

func TestRunQueryOptionalArgumentsAndStrictValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]any
		code  string
	}{
		{"omitted", nil, ""},
		{"explicit default", map[string]any{"accessLevel": "default"}, ""},
		{"all optional", map[string]any{"accessLevel": "auto_approve", "chatName": "测试", "mustUseSkills": []any{"demo"}}, ""},
		{"empty skills", map[string]any{"mustUseSkills": []any{}}, ""},
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
			result, err := handler.Invoke(context.Background(), QueryToolName, args, runToolExecContext("alice", "tool"))
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
				result, err = handler.Invoke(context.Background(), QueryToolName, args, runToolExecContext("alice", "tool"))
				if err != nil || result.Error != "" || service.starts != 1 {
					t.Fatalf("retry not idempotent: %#v %v starts=%d", result, err, service.starts)
				}
			}
		})
	}
}
