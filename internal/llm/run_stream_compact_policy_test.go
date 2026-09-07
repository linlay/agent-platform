package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAutomaticCompactThresholdAndPostL1Decision(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
		l1, l2        bool
	}{
		{"below80", 7999, 7000, false, false}, {"at80", 8000, 7000, true, false},
		{"below90", 8999, 7500, true, false}, {"at90", 9000, 4500, true, false},
		{"92to45", 9200, 4500, true, false}, {"92to91", 9200, 9100, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &llmRunStream{session: contracts.QuerySession{ChatID: "c", RunID: "r"}, model: models.ModelDefinition{ContextWindow: 10000}, pinnedMessageStart: 0, pinnedMessageEnd: 1, messages: []openAIMessage{{Role: "user", Content: "root"}}}
			s.messages = append(s.messages, openAIMessage{Role: "assistant", Content: ""}, openAIMessage{Role: "assistant", ToolCalls: []contracts.ModelToolCall{{ID: "old", Type: "function", Function: contracts.ModelFunctionCall{Name: "bash", Arguments: "{}"}}}}, openAIMessage{Role: "tool", ToolCallID: "old", Content: strings.Repeat("x", 4000)})
			appendRecentCompactTestTools(s, 5)
			// Construct exact estimated sizes instead of relying on incidental JSON overhead.
			projected, _, _ := s.compactRunToolMessages(5, 0)
			retainedOverhead := estimateModelContext(projected, nil)
			s.messages[1].Content = strings.Repeat("p", max(0, (tc.after-retainedOverhead)*4))
			projected, _, _ = s.compactRunToolMessages(5, 0)
			if post := estimateModelContext(projected, nil); post != tc.after {
				t.Fatalf("fixture post=%d", post)
			}
			base := estimateModelContext(s.messages, nil)
			body := fmt.Sprint(s.messages[3].Content)
			s.messages[3].Content = strings.Repeat("x", len(body)+(tc.before-base)*4)
			if pre := s.estimatedNextCallSize(); pre != tc.before {
				t.Fatalf("fixture pre=%d", pre)
			}
			if scheduled := s.scheduleContextCompact(false); scheduled != tc.l1 {
				t.Fatalf("scheduled=%v", scheduled)
			}
			if !tc.l1 {
				return
			}
			if s.compactWork.request.Level != "l1_tools" {
				t.Fatal("not L1 first")
			}
			if err := s.executeContextCompact(); err != nil {
				t.Fatal(err)
			}
			if (s.compactWork != nil) != tc.l2 {
				t.Fatalf("L2=%v want=%v", s.compactWork != nil, tc.l2)
			}
			for _, delta := range s.pending {
				if compact, ok := delta.(contracts.DeltaContextCompact); ok && compact.Status == "complete" {
					if compact.CycleID == "" || compact.CycleComplete == tc.l2 {
						t.Fatal("incorrect cycle completion")
					}
				}
			}
		})
	}
}

func TestManualCompactFailureAtFinalBoundaryStillRunsAutomaticGuard(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "r")
	defer control.Finish()
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	s := &llmRunStream{ctx: control.Context(), runControl: control, session: contracts.QuerySession{ChatID: "c", RunID: "r"}, model: models.ModelDefinition{ContextWindow: 2000}, pinnedMessageStart: 0, pinnedMessageEnd: 1,
		messages: []openAIMessage{{Role: "user", Content: "root"}, {Role: "assistant", Content: strings.Repeat("history ", 4000)}}}
	control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "manual", CompactID: "manual-cp", Trigger: "manual", Level: "summary"})
	if !s.scheduleContextCompact(true) {
		t.Fatal("manual request not scheduled")
	}
	s.pending = nil // consume start
	if err := s.executeContextCompact(); err != nil {
		t.Fatal(err)
	}
	if s.finished || !s.compactFinishPending || control.State() != contracts.RunLoopStateWaitingSubmit {
		t.Fatal("manual failure did not restore state")
	}
	// Persistence/Run executor resolves the manual response before asking for
	// the next delta. Only then may the independent automatic guard execute.
	control.CompleteCompact("manual", api.CompactResponse{Status: "failed", Detail: "summary_input_too_large"})
	s.pending = nil
	if err := s.fillNextPendingSource(); err != nil {
		t.Fatal(err)
	}
	if s.compactWork == nil || s.compactWork.request.Trigger != "auto" || s.compactWork.request.Level != "summary" {
		t.Fatal("manual failure bypassed automatic guard")
	}
}

func TestManualL1SkipDoesNotBypassAutomatic90Guard(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "r")
	defer control.Finish()
	s := &llmRunStream{runControl: control, session: contracts.QuerySession{ChatID: "c", RunID: "r"}, model: models.ModelDefinition{ContextWindow: 2000}, pinnedMessageStart: 0, pinnedMessageEnd: 1,
		messages: []openAIMessage{{Role: "user", Content: "root"}, {Role: "assistant", Content: strings.Repeat("history ", 4000)}}}
	handle, _ := control.EnqueueCompact(contracts.CompactControlRequest{RequestID: "manual", CompactID: "manual-cp", Trigger: "manual", Level: "l1_tools"})
	if !s.scheduleContextCompact(false) || s.compactWork == nil || s.compactWork.request.Trigger != "auto" || s.compactWork.request.Level != "summary" {
		t.Fatal("manual no-tools skipped automatic guard")
	}
	if handle.Result().Detail != "no_compactable_tools" {
		t.Fatal("manual skip result lost")
	}
}

func TestSummaryMayCoverL1ProtectedCompletedToolButNeverCrossRunPairs(t *testing.T) {
	s := &llmRunStream{model: models.ModelDefinition{ContextWindow: 2000}, pinnedMessageStart: 0, pinnedMessageEnd: 1, messages: []openAIMessage{
		{Role: "user", Content: "root anchor"},
		{Role: "assistant", OriginRunID: "r", OriginActor: "agent", ToolCalls: []contracts.ModelToolCall{{ID: "same", Function: contracts.ModelFunctionCall{Name: "file_read", Arguments: "{}"}}}},
		{Role: "tool", OriginRunID: "r", OriginActor: "agent", ToolCallID: "same", Content: strings.Repeat("large tool output ", 1000)},
	}}
	if _, cleared, _ := s.compactRunToolMessages(5, 0); cleared != 0 {
		t.Fatal("L1 released the protected tool")
	}
	plan := s.buildContextCompactPlan(true)
	if len(plan.candidates) != 2 || len(plan.pinned) != 1 {
		t.Fatal("L2 could not summarize the protected complete group")
	}
	for _, field := range []string{"run", "actor"} {
		if field == "run" {
			s.messages[2].OriginRunID = "other"
		} else {
			s.messages[2].OriginRunID = "r"
			s.messages[2].OriginActor = "other"
		}
		plan = s.buildContextCompactPlan(true)
		if len(plan.candidates) != 0 || len(plan.retained) != 2 {
			t.Fatal("L2 paired different execution owners")
		}
	}
}

func TestAutomaticCompactNoToolsWaitsUntil90AndDoesNotRepeatNoop(t *testing.T) {
	s := &llmRunStream{session: contracts.QuerySession{ChatID: "c", RunID: "r"}, model: models.ModelDefinition{ContextWindow: 10000}, pinnedMessageStart: 0, pinnedMessageEnd: 1, messages: []openAIMessage{{Role: "user", Content: "root"}, {Role: "assistant", Content: strings.Repeat("p", 34000)}}}
	if s.scheduleContextCompact(false) {
		t.Fatal("L2 started below 90")
	}
	fingerprint := s.lastNoopToolsFingerprint
	if fingerprint == "" || s.scheduleContextCompact(false) || len(s.pending) != 0 {
		t.Fatal("repeated no-op produced events")
	}
	s.messages[1].Content = strings.Repeat("p", 37000)
	if !s.scheduleContextCompact(false) || s.compactWork.request.Level != "summary" {
		t.Fatal("no tools at 90 must summarize")
	}
}

func TestManualSummaryTooLargeRestoresContextAndAutoFails(t *testing.T) {
	for _, trigger := range []string{"manual", "auto"} {
		t.Run(trigger, func(t *testing.T) {
			s := &llmRunStream{model: models.ModelDefinition{ContextWindow: 2000}, messages: []openAIMessage{{Role: "user", Content: "root"}}}
			s.compactWork = &contextCompactWork{request: contracts.CompactControlRequest{Trigger: trigger, Level: "summary"}, plan: contextCompactPlan{pinned: s.messages, candidates: []openAIMessage{{Role: "assistant", Content: strings.Repeat("too large ", 3000)}}}}
			// No engine is installed: passing this preflight cannot issue a model request.
			if err := s.executeContextCompact(); err != nil {
				t.Fatal(err)
			}
			if s.finished != (trigger == "auto") {
				t.Fatalf("finished=%v", s.finished)
			}
			if len(s.messages) != 1 || s.messages[0].Content != "root" {
				t.Fatal("failed summary changed context")
			}
			for _, d := range s.pending {
				if c, ok := d.(contracts.DeltaContextCompact); ok && c.Status == "complete" {
					t.Fatal("failed summary completed")
				}
			}
		})
	}
}
