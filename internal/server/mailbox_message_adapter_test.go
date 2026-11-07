package server

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/mailbox"
	runtimetypes "agent-platform/internal/runtime/types"
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestOrdinaryQueryIsAcceptedByMailboxBeforeRun(t *testing.T) {
	f := newTestFixture(t)
	rec := postPrimaryQuery(t, f.server, map[string]any{"agentKey": "mock-agent", "message": "hello", "requestId": "legacy-mailbox"})
	if rec.Code != http.StatusOK {
		t.Fatalf("query status=%d %s", rec.Code, rec.Body.String())
	}
	mail, err := f.server.deps.Mailbox.Store.UserRequest("", "legacy-mailbox")
	if err != nil || mail.OriginType != "user" || mail.ConsumedAt == 0 || mail.RunID == "" {
		t.Fatalf("query bypassed mailbox: %+v %v", mail, err)
	}
}

func TestExecuteDisconnectWithdrawsQueuedLetterWithoutInterruptingBusyRun(t *testing.T) {
	f, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		t.Error("cancelled queued execute reached model")
	})
	const chatID = "execute-busy"
	if _, _, err := f.chats.EnsureChatWithSourceAndMode(chatID, executeTestAgent, "busy", "query", "EXECUTOR"); err != nil {
		t.Fatal(err)
	}
	f.runs.Register(t.Context(), contracts.QuerySession{RunID: "existing-busy", ChatID: chatID, AgentKey: executeTestAgent, RunOwner: contracts.AgentRunOwner(executeTestAgent)})
	defer f.runs.Finish("existing-busy")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.server.deps.Runtime.Execute(ctx, runtimetypes.ExecuteCommand{AgentKey: executeTestAgent, ChatID: chatID, RequestID: "execute-queued", Message: "work", OutputSchema: executeTestSchema})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	var row mailbox.Mail
	for time.Now().Before(deadline) {
		row, _ = f.server.deps.Mailbox.Store.UserRequest("", "execute-queued")
		if row.Status == mailbox.Queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if row.Status != mailbox.Queued || row.RunID != "" {
		t.Fatalf("execute was not queued: %+v", row)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not stop waiting")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, _ = f.server.deps.Mailbox.Store.Get(row.ID)
		if row.Status == mailbox.Withdrawn {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if row.Status != mailbox.Withdrawn || calls.Load() != 0 {
		t.Fatalf("cancelled queue: %+v calls=%d", row, calls.Load())
	}
	if status, ok := f.runs.RunStatus("existing-busy"); !ok || status.CompletedAt != 0 {
		t.Fatalf("unrelated busy Run interrupted: %+v", status)
	}
}

func TestExecuteDisconnectInterruptsOnlyItsStartedRun(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	f, _ := newExecuteFixture(t, func(_ int32, _ executeModelRequest, _ http.ResponseWriter) { started <- struct{}{}; <-release })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.server.deps.Runtime.Execute(ctx, runtimetypes.ExecuteCommand{AgentKey: executeTestAgent, Message: "work", RequestID: "running-execute", OutputSchema: executeTestSchema})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not start")
	}
	row, err := f.server.deps.Mailbox.Store.UserRequest("", "running-execute")
	if err != nil || row.Delivery != "started" || row.RunID == "" {
		t.Fatalf("missing started letter: %+v %v", row, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("disconnect=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller did not stop waiting")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := f.runs.RunStatus(row.RunID)
		if status.State == contracts.RunLoopStateCancelled {
			unblock()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("execute Run was not interrupted")
}
