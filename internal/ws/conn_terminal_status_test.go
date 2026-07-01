package ws

import (
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestConnReleaseTerminalStatusStreamValidatesKind(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 8, MaxObservesPerConn: 3}, time.Second, AuthSession{})
	if _, err := conn.ReserveStream("run_req", "run_1"); err != nil {
		t.Fatalf("reserve run stream: %v", err)
	}
	if _, ok := conn.ReleaseTerminalStatusStream("run_req"); ok {
		t.Fatalf("expected status release to reject run stream")
	}
	conn.ReleaseStream("run_req")

	if err := conn.ReserveTerminalStream("term_req", "term_1"); err != nil {
		t.Fatalf("reserve terminal stream: %v", err)
	}
	if _, ok := conn.ReleaseTerminalStatusStream("term_req"); ok {
		t.Fatalf("expected status release to reject terminal output stream")
	}
	if _, ok := conn.ReleaseTerminalStream("term_req", "term_1"); !ok {
		t.Fatalf("expected terminal stream cleanup")
	}

	if err := conn.ReserveTerminalStatusStream("status_req"); err != nil {
		t.Fatalf("reserve status stream: %v", err)
	}
	detached, ok := conn.ReleaseTerminalStatusStream("status_req")
	if !ok {
		t.Fatalf("expected status stream release")
	}
	if detached.StreamRequestID != "status_req" || detached.StreamID != "terminal-status" {
		t.Fatalf("unexpected status detach result %#v", detached)
	}
}
