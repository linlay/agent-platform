package stream

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func awaitingOptionDescriptionBoundaries() []struct {
	name      string
	normalize func(*testing.T, map[string]any) map[string]any
} {
	return []struct {
		name      string
		normalize func(*testing.T, map[string]any) map[string]any
	}{
		{"NewEvent", func(t *testing.T, payload map[string]any) map[string]any {
			return NewEvent("awaiting.ask", payload).Payload
		}},
		{"StreamEvent.Data", func(t *testing.T, payload map[string]any) map[string]any {
			return (StreamEvent{Type: "awaiting.ask", Payload: payload}).Data().Payload
		}},
		{"ParseEventDataMap", func(t *testing.T, payload map[string]any) map[string]any {
			raw := clonePayload(payload)
			raw["type"], raw["timestamp"] = "awaiting.ask", int64(1_700_000_000_000)
			event, err := ParseEventDataMap(raw, "test.approval")
			if err != nil {
				t.Fatal(err)
			}
			return event.Payload
		}},
		{"EventData.Map", func(t *testing.T, payload map[string]any) map[string]any {
			return (EventData{Type: "awaiting.ask", Payload: payload}).Map()
		}},
		{"EventData.MarshalJSON", func(t *testing.T, payload map[string]any) map[string]any {
			data, err := json.Marshal(EventData{Type: "awaiting.ask", Timestamp: 1_700_000_000_000, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			return raw
		}},
	}
}

func TestApprovalOptionDescriptionsAreRemovedAtEventBoundaries(t *testing.T) {
	boundaries := awaitingOptionDescriptionBoundaries()
	for _, typedApprovals := range []bool{false, true} {
		for _, typedOptions := range []bool{false, true} {
			for _, boundary := range boundaries {
				t.Run(fmt.Sprintf("%s/approvalsTyped=%t/optionsTyped=%t", boundary.name, typedApprovals, typedOptions), func(t *testing.T) {
					options := []map[string]any{
						{"decision": "approve", "label": "ACP label", "description": "obsolete option text"},
						{"decision": "approve_rule_run", "description": "obsolete run text"},
					}
					approval := map[string]any{"id": "cmd-1", "command": "echo ok", "description": "approval title", "options": approvalTestObjects(options, typedOptions)}
					payload := map[string]any{
						"mode":      "approval",
						"approvals": approvalTestObjects([]map[string]any{approval}, typedApprovals),
					}
					before, _ := json.Marshal(payload)
					got := boundary.normalize(t, payload)
					actual, err := json.Marshal(got["approvals"])
					if err != nil {
						t.Fatal(err)
					}
					want := `[{"command":"echo ok","description":"approval title","id":"cmd-1","options":[{"decision":"approve","label":"ACP label"},{"decision":"approve_rule_run"}]}]`
					if string(actual) != want {
						t.Fatalf("approval wire = %s, want %s", actual, want)
					}
					after, _ := json.Marshal(payload)
					if string(before) != string(after) {
						t.Fatalf("producer payload mutated: before=%s after=%s", before, after)
					}
				})
			}
		}
	}
}

func TestPlanningOptionDescriptionsAreRemovedAtEventBoundaries(t *testing.T) {
	for _, typedOptions := range []bool{false, true} {
		for _, boundary := range awaitingOptionDescriptionBoundaries() {
			t.Run(fmt.Sprintf("%s/optionsTyped=%t", boundary.name, typedOptions), func(t *testing.T) {
				options := []map[string]any{
					{"decision": "approve", "description": "obsolete approve text"},
					{"decision": "reject", "label": "Reject", "description": "obsolete reject text", "input": map[string]any{"type": "text", "placeholder": "Explain why"}},
				}
				payload := map[string]any{
					"mode": "planning",
					"planning": map[string]any{
						"id": "confirm", "planningId": "plan-1", "title": "Review plan", "options": approvalTestObjects(options, typedOptions),
					},
				}
				before, _ := json.Marshal(payload)
				got := boundary.normalize(t, payload)
				actual, err := json.Marshal(got["planning"])
				if err != nil {
					t.Fatal(err)
				}
				want := `{"id":"confirm","options":[{"decision":"approve"},{"decision":"reject","input":{"placeholder":"Explain why","type":"text"},"label":"Reject"}],"planningId":"plan-1","title":"Review plan"}`
				if string(actual) != want {
					t.Fatalf("planning wire = %s, want %s", actual, want)
				}
				after, _ := json.Marshal(payload)
				if string(before) != string(after) {
					t.Fatalf("producer payload mutated: before=%s after=%s", before, after)
				}
			})
		}
	}
}

func TestApprovalOptionCleanupPreservesQuestionAndBusinessDescriptions(t *testing.T) {
	options := []any{map[string]any{"label": "Continue", "description": "question tooltip"}}
	for _, tc := range []struct {
		name      string
		eventType string
		payload   map[string]any
	}{
		{"question", "awaiting.ask", map[string]any{"mode": "question", "questions": []any{map[string]any{"id": "q1", "options": options}}}},
		{"form", "awaiting.ask", map[string]any{"mode": "form", "forms": []any{map[string]any{"form": map[string]any{"approvals": options}}}}},
		{"tool result", "tool.result", map[string]any{"approvals": []any{map[string]any{"options": options}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewEvent(tc.eventType, tc.payload).Data().Payload
			if !reflect.DeepEqual(got, tc.payload) {
				t.Fatalf("unrelated content changed: got=%#v want=%#v", got, tc.payload)
			}
		})
	}
}

func approvalTestObjects(items []map[string]any, typed bool) any {
	if typed {
		return items
	}
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}
