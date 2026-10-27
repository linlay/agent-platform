package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func TestMailEnvelopeIsModelOnly(t *testing.T) {
	raw := map[string]any{"role": "user", "content": "original input"}
	session := contracts.QuerySession{MailHeaders: func(string) string { return "[Letter mail_1]\n" }}
	decorated := mailModelInput(raw, "mail_1", session)
	if raw["content"] != "original input" || decorated["content"] != "[Letter mail_1]\noriginal input" {
		t.Fatalf("%#v %#v", raw, decorated)
	}
}
func TestDelegatedApprovalRejectsMandatoryHumanReview(t *testing.T) {
	// The tool review contract is opt-in. A parent at full_access cannot
	// turn a mandatory handler approval into a model decision.
	request := approvalRequest{toolApproval: &contracts.ToolApproval{AllowAutoApprove: false}}
	s := &llmRunStream{execCtx: &contracts.ExecutionContext{Session: contracts.QuerySession{AccessLevel: "full_access"}}, runControl: contracts.NewRunControl(context.Background(), "r")}
	s.runControl.SetInitialAccessLevel("full_access")
	if s.canAutoApproveTool(request) {
		t.Fatal("mandatory human review was auto-approved")
	}
}
func TestPublicSubmitCannotForgeDelegation(t *testing.T) {
	var req queryinput.SubmitRequest
	for _, field := range []string{`"origin":{"type":"agent"}`, `"mailHop":0`, `"accessGrant":{}`} {
		body := `{"runId":"r","awaitingId":"q","params":[{"answer":"yes"}],` + field + `}`
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Fatal("forged trusted submit metadata accepted", field)
		}
	}
	if !strings.Contains(mailModelInput(map[string]any{"role": "user", "content": "x"}, "m", contracts.QuerySession{MailHeaders: func(string) string { return "[Letter m]" }})["content"].(string), "Letter") {
		t.Fatal("envelope missing")
	}
}

func TestDelegatedToolApprovalReevaluatesLiveLevel(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, level := range []string{"default", "auto_approve", "full_access"} {
			t.Run(level+"/auto="+fmt.Sprint(allow), func(t *testing.T) {
				executor := &exactApprovalExecutor{allowAutoApprove: allow}
				session := contracts.QuerySession{RunID: "child", ChatID: "child-chat", AgentKey: "worker", AccessLevel: "default"}
				s := &llmRunStream{ctx: context.Background(), session: session, engine: &LLMAgentEngine{tools: executor}, execCtx: &contracts.ExecutionContext{Session: session, AccessLevel: "default"}}
				call := &preparedToolInvocation{toolID: "frozen-call", toolName: "catalog_manage", args: map[string]any{"action": "apply"}}
				delegation := s.mailApprovalDelegator(call)
				err := delegation(context.Background(), level, queryinput.SubmitRequest{Param: queryinput.SubmitParam{"decision": "approve", "data": map[string]any{}}})
				wantApproval := allow && level != "default"
				if (err == nil) != wantApproval {
					t.Fatalf("allowed=%v level=%s: %v", allow, level, err)
				}
				if len(executor.invocations) != 0 {
					t.Fatal("permission evaluation executed the operation")
				}
			})
		}
	}
}

func TestMailEnvelopeNeverPersistsInCompactionSnapshots(t *testing.T) {
	session := contracts.QuerySession{MailHeaders: func(string) string { return "[Letter m]\n" }}
	for _, body := range []any{"original", []any{map[string]any{"type": "text", "text": "original"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,x"}}}} {
		input := mailModelInput(map[string]any{"role": "user", "content": body}, "m", session)
		model := rawMessageToOpenAI(input, false)
		checkpoint := modelMessagesToMaps([]openAIMessage{model})
		encoded, _ := json.Marshal(checkpoint)
		if strings.Contains(string(encoded), "[Letter") || checkpoint[0]["_mailId"] != "m" {
			t.Fatalf("envelope leaked into checkpoint %s", encoded)
		}
		traced, _ := json.Marshal(rawMessageFromOpenAIMessage(model))
		if strings.Contains(string(traced), "[Letter") {
			t.Fatalf("envelope leaked into raw snapshot %s", traced)
		}
		restored := mailModelInput(checkpoint[0], "m", session)
		restored = mailModelInput(restored, "m", session)
		text, _ := json.Marshal(restored["content"])
		if strings.Count(string(text), "[Letter m]") != 1 {
			t.Fatalf("bad reconstructed envelope %s", text)
		}
	}
}
