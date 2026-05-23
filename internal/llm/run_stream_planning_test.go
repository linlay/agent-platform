package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func TestPlanningWriteArgumentsStreamPlanningDeltas(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID:    "chat_1",
			RunID:     "run_1",
			RequestID: "req_1",
			AgentKey:  "coder",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}

	chunks := []string{
		`{"title":"Streaming Plan",`,
		`"summary":"Stream the plan while the tool arguments arrive.",`,
		`"publicEventsAndStorage":["Emit planning start before completion"],`,
		`"implementationChanges":["Parse arguments incrementally","Write the final markdown"],`,
		`"interfaces":["Use planning_write structured fields"],`,
		`"testPlan":["Assert multiple deltas"],`,
		`"assumptions":["The provider emits tool arguments in order"]}`,
	}
	for _, chunk := range chunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      "planning_write",
			ArgsDelta: chunk,
		}})
	}

	markdown := planutil.RenderMarkdown(planutil.Spec{
		Title:                  "Streaming Plan",
		Summary:                "Stream the plan while the tool arguments arrive.",
		PublicEventsAndStorage: []string{"Emit planning start before completion"},
		ImplementationChanges:  []string{"Parse arguments incrementally", "Write the final markdown"},
		Interfaces:             []string{"Use planning_write structured fields"},
		TestPlan:               []string{"Assert multiple deltas"},
		Assumptions:            []string{"The provider emits tool arguments in order"},
	})
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_1_planning",
			"planningFile": filepath.Join(chatsDir, "plans", "run_1_planning.md"),
			"title":        "Streaming Plan",
			"status":       "ready",
			"markdown":     markdown,
		},
	})

	starts, deltaCount, ends, combined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want at least 3; events %#v", deltaCount, stream.pending)
	}
	if ends != 1 {
		t.Fatalf("planning.end count = %d, want 1", ends)
	}
	if combined != markdown {
		t.Fatalf("combined planning.delta markdown mismatch\nwant:\n%s\ngot:\n%s", markdown, combined)
	}
}

func TestPlanningWriteStreamsPartialStringsAndDraftFile(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID:    "chat_1",
			RunID:     "run_partial",
			RequestID: "req_partial",
			AgentKey:  "coder",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}

	prefixChunks := []string{
		`{"title":"Streaming Plan","summary":"Stream`,
		` the plan while`,
		` arguments arrive`,
	}
	for _, chunk := range prefixChunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      "planning_write",
			ArgsDelta: chunk,
		}})
	}

	starts, deltaCount, _, combined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want partial-string streaming; markdown %q", deltaCount, combined)
	}
	if !strings.Contains(combined, "Stream the plan while arguments arrive") {
		t.Fatalf("expected partial summary in live markdown, got:\n%s", combined)
	}
	if strings.Contains(combined, "## Public Events And Storage") {
		t.Fatalf("did not expect later sections before summary closes, got:\n%s", combined)
	}

	planningFile := filepath.Join(chatsDir, "plans", "run_partial_planning.md")
	draftBytes, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read draft planning file: %v", readErr)
	}
	if string(draftBytes) != combined {
		t.Fatalf("draft file mismatch\nwant:\n%s\ngot:\n%s", combined, string(draftBytes))
	}

	suffixChunks := []string{
		`.","publicEventsAndStorage":["Emit planning deltas before`,
		` the string closes"],"implementationChanges":["Parse arguments incrementally",`,
		`"Write the final markdown"],"interfaces":["Use planning_write structured fields"],"testPlan":["Assert multiple deltas"],`,
		`"assumptions":["The provider emits tool arguments in order"]}`,
	}
	for _, chunk := range suffixChunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      "planning_write",
			ArgsDelta: chunk,
		}})
	}

	markdown := planutil.RenderMarkdown(planutil.Spec{
		Title:                  "Streaming Plan",
		Summary:                "Stream the plan while arguments arrive.",
		PublicEventsAndStorage: []string{"Emit planning deltas before the string closes"},
		ImplementationChanges:  []string{"Parse arguments incrementally", "Write the final markdown"},
		Interfaces:             []string{"Use planning_write structured fields"},
		TestPlan:               []string{"Assert multiple deltas"},
		Assumptions:            []string{"The provider emits tool arguments in order"},
	})
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_partial_planning",
			"planningFile": planningFile,
			"title":        "Streaming Plan",
			"status":       "ready",
			"markdown":     markdown,
		},
	})

	_, _, ends, finalCombined := planningEventStats(stream.pending)
	if ends != 1 {
		t.Fatalf("planning.end count = %d, want 1", ends)
	}
	if finalCombined != markdown {
		t.Fatalf("combined planning.delta markdown mismatch\nwant:\n%s\ngot:\n%s", markdown, finalCombined)
	}
	finalBytes, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read final draft planning file: %v", readErr)
	}
	if string(finalBytes) != markdown {
		t.Fatalf("final draft file mismatch\nwant:\n%s\ngot:\n%s", markdown, string(finalBytes))
	}
}

func TestPlanningWriteCompleteArgumentsSplitIntoMultipleDeltas(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID:    "chat_1",
			RunID:     "run_1",
			RequestID: "req_1",
			AgentKey:  "coder",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}
	args := map[string]any{
		"title":                  "One Shot Plan",
		"summary":                "The provider returned full arguments in one chunk.",
		"publicEventsAndStorage": []string{"Split rendered markdown by section"},
		"implementationChanges":  []string{"Emit several planning delta events"},
		"interfaces":             []string{"Use the new planning_write schema"},
		"testPlan":               []string{"Check delta count"},
		"assumptions":            []string{"The final tool write succeeds"},
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
		ID:        "tool_plan",
		Name:      "planning_write",
		ArgsDelta: string(data),
	}})

	_, deltaCount, _, combined := planningEventStats(stream.pending)
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want multiple planning chunks; markdown %q", deltaCount, combined)
	}
	markdown := planutil.RenderMarkdown(planutil.Spec{
		Title:                  "One Shot Plan",
		Summary:                "The provider returned full arguments in one chunk.",
		PublicEventsAndStorage: []string{"Split rendered markdown by section"},
		ImplementationChanges:  []string{"Emit several planning delta events"},
		Interfaces:             []string{"Use the new planning_write schema"},
		TestPlan:               []string{"Check delta count"},
		Assumptions:            []string{"The final tool write succeeds"},
	})
	if combined != markdown {
		t.Fatalf("combined planning.delta markdown mismatch\nwant:\n%s\ngot:\n%s", markdown, combined)
	}
	for _, section := range []string{"# One Shot Plan", "## Summary", "## Public Events And Storage", "## Implementation Changes", "## Interfaces", "## Test Plan", "## Assumptions"} {
		if !strings.Contains(combined, section) {
			t.Fatalf("expected combined delta to contain %q, got:\n%s", section, combined)
		}
	}
}

func planningEventStats(events []contracts.AgentDelta) (starts int, deltas int, ends int, markdown string) {
	var b strings.Builder
	for _, event := range events {
		switch typed := event.(type) {
		case contracts.DeltaPlanningStart:
			starts++
		case contracts.DeltaPlanningDelta:
			deltas++
			b.WriteString(typed.Delta)
		case contracts.DeltaPlanningEnd:
			ends++
		}
	}
	return starts, deltas, ends, b.String()
}
