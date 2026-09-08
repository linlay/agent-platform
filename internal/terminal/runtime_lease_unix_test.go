//go:build !windows

package terminal

import (
	"testing"
	"time"
)

func TestTerminalExitOwnsOnlyOriginalRuntimeLease(t *testing.T) {
	m := NewManager()
	exit := make(chan struct{}, 2)
	req := OpenRequest{OwnerKey: "owner", AgentKey: "demo", TerminalKey: "main", CWD: t.TempDir(), Shell: "/bin/sh", OnExit: func() { exit <- struct{}{} }}
	first, err := m.Open(req)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Session.Close("closed")
	reusedExit := make(chan struct{}, 1)
	req.OnExit = func() { reusedExit <- struct{}{} }
	second, err := m.Open(req)
	if err != nil || !second.Reused {
		t.Fatalf("expected reuse: %v", err)
	}
	// A transport may fail before Start. Discard must still reap the process
	// and release its original lease; the caller releases the unused new lease.
	m.Discard(first.Session)
	select {
	case <-exit:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime lease leaked before terminal Start")
	}
	select {
	case <-reusedExit:
		t.Fatal("reused terminal stole the original lease")
	default:
	}
	first.Session.finishSubscribers()
	select {
	case <-exit:
		t.Fatal("lease released more than once")
	default:
	}
}
