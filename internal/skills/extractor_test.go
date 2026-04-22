package skills

import (
	"testing"

	"agent-platform-runner-go/internal/chat"
)

func TestCandidateFromRunTraceExtractsWorkflowFields(t *testing.T) {
	trace := chat.RunTrace{
		RunID:         "run-1",
		AssistantText: "Before deploy, ensure the schedule config is reviewed.\nStep 1: update the schedule file.\nStep 2: reload the service.\nIf reload fails, rollback immediately.\nSuccess means the job is triggered and health checks pass.",
		Query: &chat.QueryLine{
			Query: map[string]any{"message": "部署并验证 schedule 配置变更"},
		},
	}

	candidate, ok := CandidateFromRunTrace(trace, "agent-a", "chat-1")
	if !ok {
		t.Fatalf("expected procedural trace to produce candidate")
	}
	if candidate.Intent != "部署并验证 schedule 配置变更" {
		t.Fatalf("unexpected intent: %#v", candidate.Intent)
	}
	if len(candidate.Preconditions) == 0 || candidate.Preconditions[0] != "Before deploy, ensure the schedule config is reviewed" {
		t.Fatalf("unexpected preconditions: %#v", candidate.Preconditions)
	}
	if len(candidate.Steps) < 2 || candidate.Steps[0] != "Step 1: update the schedule file" || candidate.Steps[1] != "Step 2: reload the service" {
		t.Fatalf("unexpected steps: %#v", candidate.Steps)
	}
	if len(candidate.FailurePatterns) == 0 || candidate.FailurePatterns[0] != "If reload fails, rollback immediately" {
		t.Fatalf("unexpected failure patterns: %#v", candidate.FailurePatterns)
	}
	if len(candidate.SuccessCriteria) == 0 || candidate.SuccessCriteria[0] != "Success means the job is triggered and health checks pass" {
		t.Fatalf("unexpected success criteria: %#v", candidate.SuccessCriteria)
	}
}
