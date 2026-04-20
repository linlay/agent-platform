package tools

import (
	"context"
	"testing"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/skills"
)

func TestSkillCandidateToolWriteAndList(t *testing.T) {
	store, err := skills.NewFileCandidateStore(t.TempDir())
	if err != nil {
		t.Fatalf("new candidate store: %v", err)
	}
	executor, err := NewRuntimeToolExecutor(config.Config{}, nil, nil, nil, store)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	execCtx := &ExecutionContext{Session: QuerySession{
		AgentKey: "agent-a",
		ChatID:   "chat-1",
		RunID:    "run-1",
	}}
	writeResult, err := executor.Invoke(context.Background(), "_skill_candidate_write_", map[string]any{
		"title":     "Rollback workflow",
		"summary":   "Run rollback checklist before redeploy.",
		"procedure": "First verify health checks, then rollback deployment, then clear cache.",
		"category":  "workflow",
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke write: %v", err)
	}
	if writeResult.Error != "" || writeResult.ExitCode != 0 {
		t.Fatalf("unexpected write result: %#v", writeResult)
	}
	listResult, err := executor.Invoke(context.Background(), "_skill_candidate_list_", map[string]any{}, execCtx)
	if err != nil {
		t.Fatalf("invoke list: %v", err)
	}
	if listResult.Structured["count"].(int) != 1 {
		t.Fatalf("expected one candidate, got %#v", listResult.Structured)
	}
}
