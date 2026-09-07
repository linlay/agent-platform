package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

// Barriers stop the writer outside FileStore's mutex so reads can observe the
// exact interval between persistence and the executor's next lifecycle step.
type awaitingBarrierStore struct {
	*awaitingReconcileFailureStore
	pending      func(chat.PendingAwaiting)
	beforeAnswer func()
}

func (s *awaitingBarrierStore) OnRunStarted(start chat.RunStart) error {
	return s.Store.(chat.RunStartRecorder).OnRunStarted(start)
}

func (s *awaitingBarrierStore) SetPendingAwaiting(chatID string, pending chat.PendingAwaiting) error {
	pending.CreatedAt -= 2000
	if err := s.Store.SetPendingAwaiting(chatID, pending); err != nil {
		return err
	}
	if s.pending != nil {
		s.pending(pending)
	}
	return nil
}

func (s *awaitingBarrierStore) AppendSubmitLine(chatID string, line chat.SubmitLine) error {
	if s.beforeAnswer != nil {
		s.beforeAnswer()
	}
	return s.awaitingReconcileFailureStore.AppendSubmitLine(chatID, line)
}

func waitAwaitingSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for awaiting barrier")
	}
}

func TestLiveApprovalTimeoutKeepsSingleBatchResultsAndValidContinuation(t *testing.T) {
	const chatID, runID = "chat-live-timeout", "run-live-timeout"
	var requests atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		call := requests.Add(1)
		if call > 1 {
			if err := validateAwaitingToolPairs(payload["messages"]); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), 400)
				return
			}
		}
		if call == 1 {
			calls := []providerToolCallSpec{}
			for i, cmd := range []string{"docker rmi image-a", "docker rmi image-b", "ls /workspace/a", "ls /workspace/b", "ls /workspace/c"} {
				calls = append(calls, providerToolCallSpec{ID: fmt.Sprintf("tool_%d", i), Name: "bash", Args: map[string]any{"command": cmd, "cwd": "/workspace"}})
			}
			writeProviderSSE(t, w, providerToolCallsFrame(t, calls), "[DONE]")
			return
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"inventory complete"},"finish_reason":"stop"}]}`, "[DONE]")
	}, testFixtureOptions{
		sandbox: &scriptedSandbox{execute: func(command, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
			if strings.HasPrefix(command, "docker rmi") {
				t.Errorf("timed out command executed: %s", command)
			}
			return contracts.SandboxExecutionResult{Stdout: "executed: " + command, Cwd: cwd}
		}},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.ReplaceAll(data, []byte("  hitl:\n    timeout: 210"), []byte("  hitl:\n    timeout: 1"))
			if err := os.WriteFile(agentPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			hooks := filepath.Join(cfg.Paths.SkillsCenterDir, "mock-skill", ".bash-hooks")
			if err := os.MkdirAll(hooks, 0700); err != nil {
				t.Fatal(err)
			}
			rule := "commands:\n  - command: docker\n    subcommands:\n      - match: rmi\n        level: 1\n        viewportType: builtin\n        viewportKey: confirm_dialog\n        ruleKey: test-docker-rmi\n"
			if err := os.WriteFile(filepath.Join(hooks, "approval.yml"), []byte(rule), 0600); err != nil {
				t.Fatal(err)
			}
		},
	})
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var pending chat.PendingAwaiting
	wrapped := &awaitingBarrierStore{awaitingReconcileFailureStore: &awaitingReconcileFailureStore{Store: fixture.chats}, pending: func(item chat.PendingAwaiting) {
		pending = item
		close(ready)
		<-release
	}}
	fixture.server.deps.Chats = wrapped
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","chatId":"`+chatID+`","runId":"`+runID+`","message":"inventory"}`)))
	}()
	defer func() { unblock(); waitAwaitingSignal(t, done) }()
	select {
	case <-ready:
	case <-done:
		t.Fatalf("query ended before awaiting: %d %s", rec.Code, rec.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for live awaiting")
	}
	ask, err := fixture.chats.LoadAwaitingAsk(chatID, pending.AwaitingID)
	if err != nil || ask == nil {
		t.Fatalf("persisted ask missing: %#v, %v", ask, err)
	}
	approvals, _ := ask.Payload["approvals"].([]any)
	if len(approvals) != 2 || contracts.AnyIntNode(ask.Payload["timeout"]) != 1 {
		t.Fatalf("expected two approvals and one-second timeout: %#v", ask)
	}
	before, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.server.loadChatDetail(context.Background(), chatID, true)
	if err != nil || detail.Awaiting == nil {
		t.Fatalf("live awaiting was lost: %#v, %v", detail.Awaiting, err)
	}
	summary, err := fixture.chats.Summary(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if gate := fixture.server.awaitingQueryGateError(chatID, summary); gate == nil || gate.code != awaitingPendingCode {
		t.Fatalf("expected awaiting gate, got %#v", gate)
	}
	queryRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(queryRec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","chatId":"`+chatID+`","message":"continue early"}`)))
	if queryRec.Code != http.StatusConflict {
		t.Fatalf("live query admitted: %d %s", queryRec.Code, queryRec.Body.String())
	}
	after, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("reading/gating an expired live awaiting changed JSONL")
	}
	runs, err := fixture.chats.ListRuns(chatID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("live run was completed by reader: %#v %v", runs, err)
	}
	unblock()
	waitAwaitingSignal(t, done)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"type":"run.error"`) {
		t.Fatalf("batch failed: %d %s", rec.Code, rec.Body.String())
	}
	answers, counts := 0, map[string]int{}
	for _, line := range decodeDeferredChatJSONL(t, fixture.chats, chatID) {
		if contracts.AnyStringNode(contracts.AnyMapNode(line["answer"])["awaitingId"]) == pending.AwaitingID {
			answers++
		}
		for _, raw := range anySliceForServerTest(line["messages"]) {
			msg := contracts.AnyMapNode(raw)
			if msg["role"] != "tool" {
				continue
			}
			id := contracts.AnyStringNode(msg["tool_call_id"])
			counts[id]++
			content, _ := json.Marshal(msg["content"])
			if id == "tool_0" || id == "tool_1" {
				if !bytes.Contains(content, []byte("hitl_timeout")) {
					t.Errorf("approval tool did not time out: %s", content)
				}
			} else if !bytes.Contains(content, []byte("executed: ls")) {
				t.Errorf("sibling result lost: %s", content)
			}
		}
	}
	if answers != 1 || len(counts) != 5 {
		t.Fatalf("answers=%d results=%v", answers, counts)
	}
	for id, count := range counts {
		if count != 1 {
			t.Errorf("duplicate result %s: %d", id, count)
		}
	}
	next := httptest.NewRecorder()
	fixture.server.ServeHTTP(next, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","chatId":"`+chatID+`","message":"continue"}`)))
	if next.Code != 200 || strings.Contains(next.Body.String(), `"type":"run.error"`) || requests.Load() != 3 {
		t.Fatalf("continuation failed: requests=%d body=%s", requests.Load(), next.Body.String())
	}
}

func validateAwaitingToolPairs(raw any) error {
	pending := map[string]bool{}
	for _, entry := range anySliceForServerTest(raw) {
		message := contracts.AnyMapNode(entry)
		if message["role"] == "tool" {
			id := contracts.AnyStringNode(message["tool_call_id"])
			if !pending[id] {
				return fmt.Errorf("duplicate or orphan tool result %s", id)
			}
			delete(pending, id)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("non-tool message interrupts pending calls %v", pending)
		}
		for _, entry := range anySliceForServerTest(message["tool_calls"]) {
			id := contracts.AnyStringNode(contracts.AnyMapNode(entry)["id"])
			if id == "" || pending[id] {
				return fmt.Errorf("invalid call ID %q", id)
			}
			pending[id] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("unanswered calls: %v", pending)
	}
	return nil
}

func TestAcceptedSubmitRetainsExecutorUntilResultsPersist(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID, awaitingID = "chat-submit-gap", "run-submit-gap", "await-submit-gap"
	seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 1, time.Now().UnixMilli()-2000)
	_, control, _ := fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: runID, ChatID: chatID, AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", "")})
	defer fixture.runs.Finish(runID)
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: awaitingID, Mode: "question"})
	ack := fixture.runs.Submit(api.SubmitRequest{RunID: runID, AwaitingID: awaitingID, SubmitID: "submitted"})
	if !ack.Accepted {
		t.Fatalf("submit rejected: %#v", ack)
	}
	if _, found := fixture.runs.LookupAwaiting(runID, awaitingID); found {
		t.Fatal("test must cover the missing awaiting interval")
	}
	for _, state := range []contracts.RunLoopState{contracts.RunLoopStateResuming, contracts.RunLoopStateToolExecuting} {
		control.TransitionState(state)
		before, err := fixture.chats.LoadJSONLContent(chatID)
		if err != nil {
			t.Fatal(err)
		}
		detail, err := fixture.server.loadChatDetail(context.Background(), chatID, true)
		if err != nil || detail.Awaiting == nil {
			t.Fatalf("pending lost in %s: %#v %v", state, detail.Awaiting, err)
		}
		after, err := fixture.chats.LoadJSONLContent(chatID)
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("read wrote results during %s", state)
		}
	}
}

func TestRecoveredAwaitingClaimBlocksConcurrentTimeoutReadAndSubmit(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID, awaitingID = "chat-claim-race", "run-claim-race", "await-claim-race"
	createdAt := time.Now().UnixMilli() - 2000
	seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 1, createdAt)
	item := chat.PendingAwaitingWithChat{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt}
	step, err := fixture.server.loadPersistedAwaitingStep(chatID, awaitingID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.server.registerRecoveredAwaitingRun(item, step)
	if err != nil {
		t.Fatal(err)
	}
	fixture.server.deferredAwaitings.Register(DeferredAwaiting{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt, Ask: step.Ask})
	observer, err := recovered.EventBus.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.MarkDone()
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	fixture.server.deps.Chats = &awaitingBarrierStore{awaitingReconcileFailureStore: &awaitingReconcileFailureStore{Store: fixture.chats}, beforeAnswer: func() { close(ready); <-release }}
	var finishErr error
	go func() {
		defer close(done)
		_, finishErr = fixture.server.finishTerminalAwaiting(item, contracts.AwaitingTimeoutAnswer("question", 1, 2), time.Now().UnixMilli())
	}()
	defer func() { unblock(); waitAwaitingSignal(t, done) }()
	waitAwaitingSignal(t, ready)
	detail, err := fixture.server.loadChatDetail(context.Background(), chatID, false)
	if err != nil || detail.Awaiting == nil {
		t.Fatalf("claimed pending lost: %#v %v", detail.Awaiting, err)
	}
	state, err := fixture.server.finishTerminalAwaiting(item, contracts.AwaitingTimeoutAnswer("question", 1, 2), time.Now().UnixMilli())
	if err != nil || state != contracts.AwaitingResolutionOwned {
		t.Fatalf("competing timeout took ownership: %v %v", state, err)
	}
	assertAwaitingSubmitConflict(t, fixture.server, chatID, runID, awaitingID, 409, "already_resolved", "already_resolved")
	if latest, err := fixture.chats.LoadLatestAwaitingSubmit(chatID, awaitingID); err != nil || latest != nil {
		t.Fatalf("loser wrote answer: %#v %v", latest, err)
	}
	unblock()
	waitAwaitingSignal(t, done)
	if finishErr != nil {
		t.Fatal(finishErr)
	}
	assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "timeout")
	events := []stream.EventData{}
	for event := range observer.Events {
		events = append(events, event)
	}
	if len(events) != 3 || events[0].Type != "awaiting.answer" || events[1].Type != "tool.result" || events[2].Type != "run.cancel" {
		t.Fatalf("duplicate/missing terminal events: %#v", events)
	}
}

func TestUnownedAwaitingConcurrentReconciliationIsIdempotent(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID, awaitingID = "chat-orphan-race", "run-orphan-race", "await-orphan-race"
	createdAt := time.Now().UnixMilli() - 2000
	seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 1, createdAt)
	item := chat.PendingAwaitingWithChat{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt}
	// Queue every reader behind the same resolution lock with the same old input.
	unlock := fixture.server.deferredAwaitings.LockResolution(chatID, runID, awaitingID)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fixture.server.finishTerminalAwaiting(item, contracts.AwaitingTimeoutAnswer("question", 1, 2), time.Now().UnixMilli()); err != nil {
				t.Error(err)
			}
		}()
	}
	unlock()
	wg.Wait()
	assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "timeout")
}

func TestRecoveredAwaitingSubmitWinsConcurrentExpiredRead(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID, awaitingID = "chat-submit-wins", "run-submit-wins", "await-submit-wins"
	createdAt := time.Now().UnixMilli()
	seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 60, createdAt)
	item := chat.PendingAwaitingWithChat{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt}
	step, err := fixture.server.loadPersistedAwaitingStep(chatID, awaitingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.registerRecoveredAwaitingRun(item, step); err != nil {
		t.Fatal(err)
	}
	fixture.server.deferredAwaitings.Register(DeferredAwaiting{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt, Ask: step.Ask})
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var writes atomic.Int32
	fixture.server.deps.Chats = &awaitingBarrierStore{awaitingReconcileFailureStore: &awaitingReconcileFailureStore{Store: fixture.chats}, beforeAnswer: func() {
		if writes.Add(1) == 1 {
			close(ready)
			<-release
		}
	}}
	rec := httptest.NewRecorder()
	go func() {
		defer close(done)
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/submit", strings.NewReader(`{"agentKey":"mock-agent","chatId":"`+chatID+`","runId":"`+runID+`","awaitingId":"`+awaitingID+`","submitId":"winning-submit","params":[{"id":"q1","answer":"continue"}]}`)))
	}()
	defer func() { unblock(); waitAwaitingSignal(t, done) }()
	waitAwaitingSignal(t, ready)
	// Advance the persisted deadline after submit has already claimed the shell.
	if err := fixture.chats.SetPendingAwaiting(chatID, chat.PendingAwaiting{RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt - 61000}); err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.server.loadChatDetail(context.Background(), chatID, false)
	if err != nil || detail.Awaiting == nil {
		t.Fatalf("submit lost pending ownership: %#v %v", detail.Awaiting, err)
	}
	assertAwaitingSubmitConflict(t, fixture.server, chatID, runID, awaitingID, 409, "already_resolved", "already_resolved")
	if writes.Load() != 1 {
		t.Fatalf("competing terminal writes: %d", writes.Load())
	}
	unblock()
	waitAwaitingSignal(t, done)
	if rec.Code != 200 {
		t.Fatalf("winning submit failed: %d %s", rec.Code, rec.Body.String())
	}
	latest, err := fixture.chats.LoadLatestAwaitingSubmit(chatID, awaitingID)
	if err != nil || latest == nil || latest.Answer["status"] != "answered" {
		t.Fatalf("user answer replaced: %#v %v", latest, err)
	}
	// The continuation may still be streaming; wait for its real completion.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status, ok := fixture.runs.RunStatus(runID); ok && status.CompletedAt > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("submitted continuation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if writes.Load() != 1 {
		t.Fatalf("duplicate answer writes: %d", writes.Load())
	}
}

func TestUnownedAwaitingRetryAfterPartialWrite(t *testing.T) {
	for _, stage := range []string{"answer", "tool_result", "completion", "clear_pending"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newTestFixture(t)
			const chatID, runID, awaitingID = "chat-write-retry", "run-write-retry", "await-write-retry"
			createdAt := time.Now().UnixMilli() - 2000
			seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 1, createdAt)
			failing := &awaitingReconcileFailureStore{Store: fixture.chats, stage: stage}
			fixture.server.deps.Chats = failing
			item := chat.PendingAwaitingWithChat{ChatID: chatID, RunID: runID, AwaitingID: awaitingID, Mode: "question", CreatedAt: createdAt}
			answer := contracts.AwaitingTimeoutAnswer("question", 1, 2)
			if _, err := fixture.server.finishTerminalAwaiting(item, answer, time.Now().UnixMilli()); err == nil {
				t.Fatal("expected injected failure")
			}
			summary, err := fixture.chats.Summary(chatID)
			if err != nil || summary == nil || summary.PendingAwaiting == nil {
				t.Fatalf("partial write cleared pending: %#v %v", summary, err)
			}
			failing.stage = ""
			if state, err := fixture.server.finishTerminalAwaiting(item, answer, time.Now().UnixMilli()); err != nil || state != contracts.AwaitingResolutionFinished {
				t.Fatalf("retry failed: %v %v", state, err)
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "timeout")
			before, err := fixture.chats.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.server.finishTerminalAwaiting(item, answer, time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			after, err := fixture.chats.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("repeated reconciliation rewrote terminal records")
			}
		})
	}
}
