package chat

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"agent-platform/internal/contracts/queryinput"
)

func TestPriorSkillReferenceIDsUsesOnlyMainLaneStructuredSelections(t *testing.T) {
	s, err := NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const chatID = "skills"
	query := func(runID, taskID, lane string, refs any, input map[string]any) {
		t.Helper()
		if err := s.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: runID,
			UpdatedAt: testEpochMillis(1), TaskID: taskID, Lane: lane, References: refs, Query: input}); err != nil {
			t.Fatal(err)
		}
	}
	steer := func(runID string, ids ...string) {
		t.Helper()
		if err := s.AppendSteerLine(chatID, SteerLine{Type: "steer", ChatID: chatID, RunID: runID,
			UpdatedAt: testEpochMillis(2), Steer: map[string]any{"chatId": chatID, "runId": runID,
				"steerId": "steer-" + runID, "message": "continue", "role": "user", "references": queryinput.MakeReferences("skill", ids)}}); err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{"message": "use the skill"}
	query("first", "", "", queryinput.MakeReferences("skill", []string{"old", "Shared"}), input)
	steer("first", "steered", "SHARED")
	query("legacy", "", "", nil, map[string]any{"message": "old", "references": queryinput.MakeReferences("skill", []string{"untrusted"})})
	query("side", "", "btw", queryinput.MakeReferences("skill", []string{"side"}), input)
	steer("side", "side-steer")
	query("task-run", "task", "", queryinput.MakeReferences("skill", []string{"child"}), input)
	steer("task-run", "child-steer")
	// TEAM task rows may share the coordinator's Run ID.
	query("first", "team-task", "", queryinput.MakeReferences("skill", []string{"team-member"}), input)
	steer("orphan", "no-main-query")
	query("latest", "", "", []map[string]any{{"type": "skill", "id": " SHARED "}, {"type": "kbase", "id": "not-skill"}}, input)
	query("current", "", "", queryinput.MakeReferences("skill", []string{"current"}), input)
	steer("current", "current-steer")
	got, err := s.PriorSkillReferenceIDs(chatID, "current", 32)
	if err != nil || !reflect.DeepEqual(got, []string{"shared", "steered", "old"}) {
		t.Fatalf("ids=%v err=%v", got, err)
	}
	got, err = s.PriorSkillReferenceIDs(chatID, "current", 2)
	if err != nil || !reflect.DeepEqual(got, []string{"shared", "steered"}) {
		t.Fatalf("limited ids=%v err=%v", got, err)
	}
	if _, err := s.PriorSkillReferenceIDs("../outside", "", 32); !os.IsPermission(err) {
		t.Fatalf("invalid Chat path accepted: %v", err)
	}
}

func TestPriorSkillReferenceIDsBoundsRecentDistinctSkills(t *testing.T) {
	s, err := NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for i := 0; i < 35; i++ {
		if err := s.AppendQueryLine("chat", QueryLine{Type: "query", ChatID: "chat", RunID: fmt.Sprint(i), UpdatedAt: testEpochMillis(int64(i)),
			Query: map[string]any{"message": "read"}, References: queryinput.MakeReferences("skill", []string{fmt.Sprintf("skill-%02d", i)})}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PriorSkillReferenceIDs("chat", "new", 32)
	if err != nil || len(got) != 32 || got[0] != "skill-34" || got[31] != "skill-03" {
		t.Fatalf("ids=%v err=%v", got, err)
	}
}
