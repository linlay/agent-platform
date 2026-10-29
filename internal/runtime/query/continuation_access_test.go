package query

import (
	"context"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/runstate"
)

func TestContinuationAccessKeepsLivePermissionAndResolvesNewExecution(t *testing.T) {
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "plan", ChatID: "chat", AccessLevel: "auto_approve"})
	defer runs.Finish("plan")
	s := NewService(Dependencies{Runs: runs})
	def := catalog.AgentDefinition{AccessConfig: contracts.AgentAccessConfig{Default: "full_access"}}
	original := &chat.QueryLine{AccessLevel: "auto_approve", Query: map[string]any{"message": "task"}}
	level, _, _ := s.continuationAccess(original, "plan", def, false)
	if level != "auto_approve" {
		t.Fatal("same Run default was re-read")
	}
	level, source, _ := s.continuationAccess(original, "plan", def, true)
	if level != "full_access" || source != "agent_default" {
		t.Fatalf("new execution: %s %s", level, source)
	}
	control.UpdateAccessLevel("default")
	level, _, _ = s.continuationAccess(original, "plan", def, true)
	if level != "default" {
		t.Fatal("user's planning override was lost")
	}
}

type accessContinuationStore struct {
	chat.Store
	line *chat.QueryLine
}

func (s accessContinuationStore) LoadRunQuery(string, string) (*chat.QueryLine, error) {
	return s.line, nil
}

func TestPlanPermissionIncreaseMustBeConfirmedBeforeAnswerIsConsumed(t *testing.T) {
	runs := runstate.NewManager()
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{RunID: "plan", ChatID: "chat", AccessLevel: "default"})
	defer runs.Finish("plan")
	original := &chat.QueryLine{AccessLevel: "default", Query: map[string]any{"message": "task"}}
	s := NewService(Dependencies{Runs: runs, Chats: accessContinuationStore{line: original}})
	admission := awaitingContinuationAdmission{AgentDef: catalog.AgentDefinition{AccessConfig: contracts.AgentAccessConfig{Default: "full_access"}}}
	req := queryinput.SubmitRequest{RunID: "plan", ChatID: "chat", Param: queryinput.SubmitParam{"decision": "approve"}}
	if err := s.preparePlanExecutionAccess(req, &admission); err == nil || admission.Frozen {
		t.Fatal("permission increase was accepted without confirmation")
	}
	req.Param["executionAccessLevel"] = "full_access"
	if err := s.preparePlanExecutionAccess(req, &admission); err != nil || !admission.Frozen || admission.AccessLevel != "full_access" {
		t.Fatalf("confirmed admission=%+v err=%v", admission, err)
	}
}
