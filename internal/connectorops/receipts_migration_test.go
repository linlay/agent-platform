package connectorops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionV2ReceiptMigrationNeverRedispatches(t *testing.T) {
	// Independently reproduce the persisted v2 format, including its old appId
	// slot. The new namespace must address exactly those bytes after migration.
	hash := func(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
	for _, complete := range []bool{false, true} {
		root := t.TempDir()
		key := hash("alice\x00calendar\x00demo\x00execution-v2\x00daily-key-123")
		fingerprint := hash(`{"Adapter":"cli","Args":["send","hello"],"Component":"","ToolName":"","Arguments":null}`)
		dir := filepath.Join(root, "demo", "invocations")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, key+".json")
		if err := os.WriteFile(file, []byte(`{"digest":"`+fingerprint+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if complete {
			raw, _ := json.Marshal(map[string]any{"digest": fingerprint, "result": Result{InvocationID: "old-result"}})
			if err := os.WriteFile(file+".done", raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		scope := Scope{Subject: "alice", IdempotencyNamespace: "calendar"}
		req := Request{ConnectorID: "demo", Adapter: "cli", Args: []string{"send", "hello"}, IdempotencyKey: "daily-key-123"}
		claimed, previous, err := beginWrite(root, scope, req)
		if claimed != "" {
			t.Fatal("migration claimed a new execution", claimed)
		}
		if complete {
			if err != nil || previous == nil || previous.InvocationID != "old-result" {
				t.Fatal(previous, err)
			}
		} else if err == nil || err.Error() != "invocation_outcome_unknown" {
			t.Fatal("incomplete legacy claim replayed", err)
		}
		req.Args[1] = "changed"
		if _, _, err = beginWrite(root, scope, req); err == nil || err.Error() != "idempotency_conflict" {
			t.Fatal("legacy parameters were not compared", err)
		}
		if _, err = os.Stat(file); err != nil {
			t.Fatal("legacy claim removed", err)
		}
	}
}
