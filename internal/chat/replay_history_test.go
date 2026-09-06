package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/timecontract"
)

// These fixtures capture the pre-refactor output, including raw messages and
// synthetic events. Archive must replay the same bytes, not an export document.
func TestHistoryReplayActiveArchiveCompatibility(t *testing.T) {
	for _, owner := range []string{"agent", "team"} {
		t.Run(owner, func(t *testing.T) {
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
			agentKey, teamID := "agent-a", ""
			if owner == "team" {
				agentKey, teamID = "", "team-a"
			}
			if _, _, err := active.EnsureChat("chat-replay", agentKey, teamID, "回放兼容"); err != nil {
				t.Fatal(err)
			}
			if _, err := active.db.Exec(`UPDATE CHATS SET CREATED_AT_=? WHERE CHAT_ID_=?`, testEpochMillis(0), "chat-replay"); err != nil {
				t.Fatal(err)
			}
			for i, reason := range []string{"complete", "error", "cancel", "complete"} {
				started, ended := int64((i+1)*100+10), int64((i+1)*100+90)
				if i == 0 {
					started, ended = 10, 90
				}
				runID := []string{"run-1", "run-2", "run-3", "run-4"}[i]
				if err := completeRunForTest(active, RunCompletion{ChatID: "chat-replay", RunID: runID, AgentKey: agentKey, TeamID: teamID, StartedAtMillis: testEpochMillis(started), UpdatedAtMillis: testEpochMillis(ended), FinishReason: reason}); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile("testdata/replay/history.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(active.chatJSONLPath("chat-replay"), original, 0600); err != nil {
				t.Fatal(err)
			}
			planDir := filepath.Join(active.ChatDir("chat-replay"), ToolRootDirName, ToolPlanTasksDirName)
			if err := os.MkdirAll(planDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(planDir, "run-4_plan.json"), []byte(`{"version":1,"chatId":"chat-replay","runId":"run-4","planId":"plan-4","updatedAt":1700000000490,"tasks":[{"taskId":"task-4","description":"Review","status":"completed"}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := active.AppendArtifactManifest("chat-replay", "run-4", testEpochMillis(490), []map[string]any{{"artifactId": "artifact-1", "type": "file", "name": "result.txt", "url": "artifacts/run-4/result.txt"}}); err != nil {
				t.Fatal(err)
			}
			detail, err := active.LoadChat("chat-replay")
			if err != nil {
				t.Fatal(err)
			}

			// UsageData deliberately omits accounting fields in JSON. Assert the
			// complete in-memory totals as well as the serialized fixture.
			costTotal := 0.06
			costTotal += 0.01
			wantUsage := ReplayUsage{
				LastRunID: "run-4", LastRun: UsageData{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5},
				Chat: UsageData{PromptTokens: 33, CompletionTokens: 15, TotalTokens: 48,
					CachedTokens: 5, ReasoningTokens: 3, PromptCacheHitTokens: 5, PromptCacheMissTokens: 15,
					LlmChatCompletionCount: 1, ToolCallCount: 2, FirstTokenLatencyTotalMs: 12, FirstTokenLatencyCount: 1, GenerationDurationMs: 20,
					EstimatedCostCurrency: "CNY", EstimatedCostInputHit: 0.02, EstimatedCostInputMiss: 0.02, EstimatedCostOutput: 0.03, EstimatedCostTotal: costTotal},
			}
			if !reflect.DeepEqual(detail.ReplayUsage, wantUsage) {
				t.Fatalf("usage=%#v, want %#v", detail.ReplayUsage, wantUsage)
			}
			got, err := json.MarshalIndent(detail, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := "testdata/replay/" + owner + ".detail.json"
			if os.Getenv("UPDATE_REPLAY_GOLDEN") == "1" {
				if err := os.WriteFile(golden, got, 0600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("history differs from %s", golden)
			}
			persisted, err := os.ReadFile(active.chatJSONLPath("chat-replay"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(persisted, original) {
				t.Fatal("loading history changed JSONL bytes")
			}
			if err := NewArchiver(active, archive).ArchiveChat("chat-replay"); err != nil {
				t.Fatal(err)
			}
			archived, err := archive.LoadArchived("chat-replay")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(archived.Detail, detail) {
				t.Fatal("archive changed complete in-memory detail")
			}
			archivedJSON, err := json.MarshalIndent(archived.Detail, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(append(archivedJSON, '\n'), want) {
				t.Fatal("archive detail differs from active detail")
			}
			if archived.JSONLContent != string(original) {
				t.Fatal("archive changed JSONL bytes")
			}
		})
	}
}

func TestReplayHistoryRunLifecycleCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, reason, terminal, storedEvent, errorField, errorLocation string
		completed, pending, missingStart                               bool
	}{
		{name: "unfinished"},
		{name: "completed", completed: true, reason: "complete", terminal: "run.complete"},
		{name: "failed", completed: true, reason: "error", terminal: "run.error"},
		{name: "cancelled", completed: true, reason: "interrupted", terminal: "run.cancel"},
		{name: "pending wait suppresses completion", completed: true, pending: true},
		{name: "missing start", missingStart: true, errorField: "startedAt", errorLocation: "chat.replay.runs[run-1].startedAt"},
		{name: "mismatched stored start", storedEvent: "run.start", errorField: "timestamp", errorLocation: "chat.replay.runs[run-1].run.start.timestamp"},
		{name: "mismatched stored completion", completed: true, storedEvent: "run.complete", errorField: "timestamp", errorLocation: "chat.replay.runs[run-1].run.complete.timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := Summary{ChatID: "chat-replay", ChatName: "history", CreatedAt: testEpochMillis(0)}
			if tc.pending {
				summary.PendingAwaiting = &PendingAwaiting{RunID: "run-1"}
			}
			lines := []map[string]any{
				{"_type": "query", "chatId": summary.ChatID, "runId": "run-1", "updatedAt": testEpochMillis(11), "liveSeq": 9, "query": map[string]any{"message": "question", "seq": 999, "liveSeq": 999}},
				{"_type": "react", "runId": "run-1", "updatedAt": testEpochMillis(20), "seq": 1, "liveSeq": 20, "awaiting": []any{map[string]any{"type": "awaiting.ask", "timestamp": testEpochMillis(19), "awaitingId": "ask-1", "mode": "question"}}},
			}
			if tc.storedEvent != "" {
				lines = append(lines, map[string]any{"_type": "event", "runId": "run-1", "event": map[string]any{"type": tc.storedEvent, "timestamp": testEpochMillis(99)}})
			}
			before, err := json.Marshal(lines)
			if err != nil {
				t.Fatal(err)
			}
			started, completed := map[string]int64{}, map[string]int64{}
			if !tc.missingStart {
				started["run-1"] = testEpochMillis(10)
			}
			if tc.completed {
				completed["run-1"] = testEpochMillis(30)
			}
			detail, err := replayChatHistory(summary, lines, nil, t.TempDir(), started, completed, map[string]string{"run-1": tc.reason})
			after, marshalErr := json.Marshal(lines)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("replay mutated input records")
			}
			if tc.errorField != "" {
				var violation *timecontract.Violation
				if !errors.As(err, &violation) || violation.Field != tc.errorField || violation.Location != tc.errorLocation {
					t.Fatalf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(detail, Detail{}) {
					t.Fatalf("error returned partial detail: %#v", detail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantTypes := []string{"chat.start", "request.query", "run.start", "awaiting.ask"}
			if tc.terminal != "" {
				wantTypes = append(wantTypes, tc.terminal)
			}
			var types []string
			for i, event := range detail.Events {
				types = append(types, event.Type)
				if event.Seq != int64(i+1) {
					t.Fatalf("noncontiguous history seq: %#v", event)
				}
			}
			if !reflect.DeepEqual(types, wantTypes) {
				t.Fatalf("types=%v, want %v", types, wantTypes)
			}
			if detail.Events[1].Timestamp != testEpochMillis(11) || detail.Events[2].Timestamp != testEpochMillis(10) || int64FromAny(detail.Events[1].Value("liveSeq")) != 9 {
				t.Fatal("query/start clocks or liveSeq changed")
			}
			if tc.terminal != "" && detail.Events[len(detail.Events)-1].Timestamp != testEpochMillis(30) {
				t.Fatal("terminal clock changed")
			}
		})
	}
}
