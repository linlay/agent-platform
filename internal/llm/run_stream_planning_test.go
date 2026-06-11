package llm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	contracts "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func TestFinalizePlanningArgumentsStreamPlanningDeltas(t *testing.T) {
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
		`{"markdown":"# Streaming Plan`,
		`\n\n## Summary\nStream the plan while the tool arguments arrive.\n\n## Public Events And Storage\n- Emit planning delta before completion\n\n## Implementation Changes\n- Parse arguments incrementally\n- Write the final markdown\n\n## Interfaces\n- Use finalize_planning markdown field\n\n## Test Plan\n- Assert multiple deltas\n\n## Assumptions\n- The provider emits tool arguments in order"}`,
	}
	for _, chunk := range chunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      contracts.FinalizePlanningToolName,
			ArgsDelta: chunk,
		}})
	}

	markdown := streamingPlanningMarkdown("Streaming Plan", "Stream the plan while the tool arguments arrive.", "Emit planning delta before completion")
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_1_planning_1",
			"planningFile": planutil.PlanningFileForChat(chatsDir, "chat_1", "run_1_planning_1"),
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

func TestFinalizePlanningStreamsPartialStringsAndDraftFile(t *testing.T) {
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
		`{"markdown":"# Streaming Plan\n\n## Summary\nStream`,
		` the plan while`,
		` arguments arrive`,
	}
	for _, chunk := range prefixChunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      contracts.FinalizePlanningToolName,
			ArgsDelta: chunk,
		}})
	}

	starts, deltaCount, ends, combined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
	if ends != 0 {
		t.Fatalf("planning.end count = %d, want 0", ends)
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

	planningFile := planutil.PlanningFileForChat(chatsDir, "chat_1", "run_partial_planning_1")
	draftBytes, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read draft planning file: %v", readErr)
	}
	if string(draftBytes) != combined {
		t.Fatalf("draft file mismatch\nwant:\n%s\ngot:\n%s", combined, string(draftBytes))
	}

	suffixChunks := []string{
		`.\n\n## Public Events And Storage\n- Emit planning deltas before the string closes`,
		`\n\n## Implementation Changes\n- Parse arguments incrementally\n- Write the final markdown`,
		`\n\n## Interfaces\n- Use finalize_planning markdown field\n\n## Test Plan\n- Assert multiple deltas`,
		`\n\n## Assumptions\n- The provider emits tool arguments in order"}`,
	}
	for _, chunk := range suffixChunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      contracts.FinalizePlanningToolName,
			ArgsDelta: chunk,
		}})
	}

	markdown := streamingPlanningMarkdown("Streaming Plan", "Stream the plan while arguments arrive.", "Emit planning deltas before the string closes")
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_partial_planning_1",
			"planningFile": planningFile,
			"markdown":     markdown,
		},
	})

	starts, _, ends, finalCombined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
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

func TestFinalizePlanningCompleteArgumentsSplitIntoMultipleDeltas(t *testing.T) {
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
		"markdown": oneShotPlanningMarkdown(),
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
		ID:        "tool_plan",
		Name:      contracts.FinalizePlanningToolName,
		ArgsDelta: string(data),
	}})

	starts, deltaCount, ends, combined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
	if ends != 0 {
		t.Fatalf("planning.end count = %d, want 0 before tool result", ends)
	}
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want multiple planning chunks; markdown %q", deltaCount, combined)
	}
	markdown := oneShotPlanningMarkdown()
	if combined != markdown {
		t.Fatalf("combined planning.delta markdown mismatch\nwant:\n%s\ngot:\n%s", markdown, combined)
	}
	for _, section := range []string{"# One Shot Plan", "## Summary", "## Public Events And Storage", "## Implementation Changes", "## Interfaces", "## Test Plan", "## Assumptions"} {
		if !strings.Contains(combined, section) {
			t.Fatalf("expected combined delta to contain %q, got:\n%s", section, combined)
		}
	}
}

func TestFinalizePlanningMarkdownStartingWithLevelTwoHeadingStreamsAsIs(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat_1",
			RunID:  "run_h2",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}
	markdown := "## Summary\nNo heading synthesis.\n\n## Test Plan\n- Assert exact markdown."
	args := map[string]any{"markdown": markdown}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
		ID:        "tool_plan",
		Name:      contracts.FinalizePlanningToolName,
		ArgsDelta: string(data),
	}})
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_h2_planning_1",
			"planningFile": planutil.PlanningFileForChat(chatsDir, "chat_1", "run_h2_planning_1"),
			"markdown":     markdown,
		},
	})

	starts, _, ends, combined := planningEventStats(stream.pending)
	if starts != 1 || ends != 1 {
		t.Fatalf("expected planning lifecycle events, starts=%d ends=%d events=%#v", starts, ends, stream.pending)
	}
	if combined != markdown {
		t.Fatalf("combined planning delta should preserve markdown exactly\nwant:%q\ngot:%q", markdown, combined)
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

func streamingPlanningMarkdown(title string, summary string, publicEvent string) string {
	return `# ` + title + `

## Summary
` + summary + `

## Public Events And Storage
- ` + publicEvent + `

## Implementation Changes
- Parse arguments incrementally
- Write the final markdown

## Interfaces
- Use finalize_planning markdown field

## Test Plan
- Assert multiple deltas

## Assumptions
- The provider emits tool arguments in order`
}

func oneShotPlanningMarkdown() string {
	return `# One Shot Plan

## Summary
The provider returned full arguments in one chunk.

## Public Events And Storage
- Split rendered markdown by section

## Implementation Changes
- Emit several planning delta events

## Interfaces
- Use the new finalize_planning schema

## Test Plan
- Check delta count

## Assumptions
- The final tool write succeeds
`
}
