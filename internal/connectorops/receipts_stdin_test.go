package connectorops

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWriteReceiptBindsStdinAndKeepsAudit(t *testing.T) {
	root := t.TempDir()
	scope := Scope{Subject: "viewer:alice", IdempotencyNamespace: "view:chat", Audit: map[string]any{"subject": "alice", "action": "transition"}}
	req := Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"transition", "--fields-file", "-"}, Stdin: `{"resolution":{"id":"1"}}`, IdempotencyKey: "write-0001"}
	file, previous, err := beginWrite(root, scope, req)
	if err != nil || previous != nil || file == "" {
		t.Fatal(file, previous, err)
	}
	raw, _ := os.ReadFile(file)
	var claim receipt
	if json.Unmarshal(raw, &claim) != nil || claim.Audit["subject"] != "alice" {
		t.Fatal(string(raw))
	}
	if _, _, err = beginWrite(root, scope, req); err == nil || err.Error() != "invocation_outcome_unknown" {
		t.Fatal("incomplete claim replayed", err)
	}
	if err = finishWrite(file, req, Result{InvocationID: "once"}); err != nil {
		t.Fatal(err)
	}
	_, previous, err = beginWrite(root, scope, req)
	if err != nil || previous == nil || previous.InvocationID != "once" {
		t.Fatal(previous, err)
	}
	req.Stdin = `{"resolution":{"id":"2"}}`
	if _, _, err = beginWrite(root, scope, req); err == nil || err.Error() != "idempotency_conflict" {
		t.Fatal("stdin not bound", err)
	}
}
