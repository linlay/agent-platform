package chat

import (
	"reflect"
	"testing"
)

// Exercise persisted Run metadata and both active/archive readers, rather than
// deriving TEAM ownership from the current Agent catalog or Chat summary.
func TestTEAMReplayPreservesMemberActors(t *testing.T) {
	for _, withQuery := range []bool{true, false} {
		for _, terminal := range []struct{ status, event string }{
			{"completed", "task.complete"}, {"failed", "task.error"}, {"cancelled", "task.cancel"}, {"", "task.complete"},
		} {
			name := terminal.event + "/" + terminal.status
			if withQuery {
				name += "/query"
			} else {
				name += "/step-only"
			}
			t.Run(name, func(t *testing.T) {
				const chatID, runID, owner = "team-replay", "run-team", "research"
				root := t.TempDir()
				active, err := NewFileStore(root)
				if err != nil {
					t.Fatal(err)
				}
				defer active.Close()
				archive, err := NewArchiveStore(root)
				if err != nil {
					t.Fatal(err)
				}
				defer archive.db.Close()
				if _, _, err = active.EnsureChatWithSourceAndMode(chatID, owner, "request", "", "TEAM"); err != nil {
					t.Fatal(err)
				}
				start := testEpochMillis(10)
				if err = active.OnRunStarted(RunStart{ChatID: chatID, RunID: runID, AgentKey: owner, AgentMode: "TEAM", StartedAtMillis: start}); err != nil {
					t.Fatal(err)
				}
				for i, member := range []string{"writer", "reviewer"} {
					ts := testEpochMillis(int64(20 + i*10))
					if withQuery {
						err = active.AppendQueryLine(chatID, QueryLine{Type: "query", ChatID: chatID, RunID: runID, UpdatedAt: ts, TaskID: member + "-task", SubAgentKey: member, Query: map[string]any{"message": "member request"}})
						if err != nil {
							t.Fatal(err)
						}
					}
					step := StepLine{Type: StepLineTypeReact, ChatID: chatID, RunID: runID, UpdatedAt: ts + 1, TaskID: member + "-task", TaskStatus: terminal.status, Messages: []StoredMessage{{Role: "assistant", Content: textContent(member + " result"), Ts: &ts}}}
					if !withQuery {
						step.TaskSubAgentKey = member
					}
					if err = active.AppendStepLine(chatID, step); err != nil {
						t.Fatal(err)
					}
				}
				ts := testEpochMillis(50)
				if err = active.AppendStepLine(chatID, StepLine{Type: StepLineTypeReact, ChatID: chatID, RunID: runID, UpdatedAt: ts, Messages: []StoredMessage{{Role: "assistant", Content: textContent("root result"), Ts: &ts}}}); err != nil {
					t.Fatal(err)
				}
				if err = active.OnRunCompleted(RunCompletion{ChatID: chatID, RunID: runID, AgentKey: owner, AgentMode: "TEAM", StartedAtMillis: start, UpdatedAtMillis: testEpochMillis(60)}); err != nil {
					t.Fatal(err)
				}
				if _, err = active.db.Exec(`UPDATE CHATS SET AGENT_MODE_='GENERAL' WHERE CHAT_ID_=?`, chatID); err != nil {
					t.Fatal(err)
				}
				check := func(label string, detail Detail) {
					t.Helper()
					seen := map[string]int{}
					for _, event := range detail.Events {
						key := event.String("taskId")
						if event.Type != "task.start" && event.Type != terminal.event && event.Type != "content.snapshot" {
							continue
						}
						expected, presentation := owner, "reply"
						if key != "" {
							presentation = "task"
							switch key {
							case "writer-task":
								expected = "writer"
							case "reviewer-task":
								expected = "reviewer"
							default:
								t.Fatalf("unexpected task %q", key)
							}
						}
						if !reflect.DeepEqual(event.Value("actor"), map[string]any{"type": "agent", "agentKey": expected}) || event.String("presentation") != presentation || event.String("agentKey") != expected {
							t.Errorf("%s %s task=%q: actor/presentation/agentKey=%#v", label, event.Type, key, event.Payload)
						}
						seen[key+"/"+event.Type]++
					}
					for _, member := range []string{"writer", "reviewer"} {
						for _, kind := range []string{"task.start", terminal.event, "content.snapshot"} {
							if seen[member+"-task/"+kind] != 1 {
								t.Errorf("%s missing or duplicate %s %s: %v", label, member, kind, seen)
							}
						}
					}
					if seen["/content.snapshot"] != 1 {
						t.Errorf("%s missing root reply: %v", label, seen)
					}
				}
				detail, err := active.LoadChat(chatID)
				if err != nil {
					t.Fatal(err)
				}
				check("active", detail)
				if err = NewArchiver(active, archive).ArchiveChat(chatID); err != nil {
					t.Fatal(err)
				}
				archived, err := archive.LoadArchived(chatID)
				if err != nil {
					t.Fatal(err)
				}
				check("archive", archived.Detail)
			})
		}
	}
}
