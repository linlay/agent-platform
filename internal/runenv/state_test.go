package runenv

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStateBindSetUnsetAndMetadataNeverRevealValues(t *testing.T) {
	store, identity, policy := testStore(t)
	state, err := store.New(identity, policy)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationBind, Name: "DOCUMENT_ID", Value: "doc-123"}}, DefaultIdempotencyKey: "run:tool-1"})
	if err != nil || !bound.Changed || bound.Revision != 1 {
		t.Fatalf("bind = %#v, %v", bound, err)
	}
	retry, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationBind, Name: "DOCUMENT_ID", Value: "doc-123"}}, DefaultIdempotencyKey: "run:tool-1"})
	if err != nil || !retry.Idempotent || retry.Revision != 1 {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	if _, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationBind, Name: "DOCUMENT_ID", Value: "doc-456"}}}); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("bind replacement error = %v", err)
	}
	set, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationSet, Name: "SESSION_TOKEN", Value: "secret-token"}}})
	if err != nil || set.Revision != 2 {
		t.Fatalf("set = %#v, %v", set, err)
	}
	items, _, err := state.List(map[string]string{"SESSION_TOKEN": "static-fallback"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "doc-123") || strings.Contains(string(raw), "secret-token") {
		t.Fatalf("metadata leaked values: %s", raw)
	}
	unset, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationUnset, Name: "SESSION_TOKEN"}}})
	if err != nil || unset.Revision != 3 {
		t.Fatalf("unset = %#v, %v", unset, err)
	}
	item, _, err := state.Get("SESSION_TOKEN", map[string]string{"SESSION_TOKEN": "static-fallback"})
	if err != nil || !item.Present || item.Source != "static" {
		t.Fatalf("fallback metadata = %#v, %v", item, err)
	}
}

func TestStateBulkIsAtomicAndRevisionChecked(t *testing.T) {
	store, identity, policy := testStore(t)
	state, err := store.New(identity, policy)
	if err != nil {
		t.Fatal(err)
	}
	expected := uint64(0)
	result, err := state.Mutate(MutationRequest{ExpectedRevision: &expected, Operations: []Mutation{
		{Operation: OperationBind, Name: "DOCUMENT_ID", Value: "doc-123"},
		{Operation: OperationSet, Name: "SESSION_TOKEN", Value: "token-a"},
	}})
	if err != nil || result.Revision != 1 {
		t.Fatalf("bulk = %#v, %v", result, err)
	}
	stale := uint64(0)
	if _, err := state.Mutate(MutationRequest{ExpectedRevision: &stale, Operations: []Mutation{{Operation: OperationSet, Name: "SESSION_TOKEN", Value: "token-b"}}}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision error = %v", err)
	}
	if _, err := state.Mutate(MutationRequest{Operations: []Mutation{
		{Operation: OperationSet, Name: "SESSION_TOKEN", Value: "token-b"},
		{Operation: OperationSet, Name: "DOCUMENT_ID", Value: "invalid-mode"},
	}}); err == nil {
		t.Fatal("expected invalid bulk to fail")
	}
	snapshot, revision, err := state.Snapshot(TargetHost, policy)
	if err != nil || revision != 1 || snapshot["SESSION_TOKEN"] != "token-a" {
		t.Fatalf("atomic snapshot = %#v rev=%d err=%v", snapshot, revision, err)
	}
}

func TestCheckpointRestoreIsAuthenticatedAndCleanupIsFinal(t *testing.T) {
	store, identity, policy := testStore(t)
	state, err := store.New(identity, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationBind, Name: "DOCUMENT_ID", Value: "doc-123"}}}); err != nil {
		t.Fatal(err)
	}
	restored, err := NewStore(store.root, store.keyFile, Limits{}).Restore(identity, policy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, revision, err := restored.Snapshot(TargetHost, policy)
	if err != nil || revision != 1 || snapshot["DOCUMENT_ID"] != "doc-123" {
		t.Fatalf("restored = %#v rev=%d err=%v", snapshot, revision, err)
	}
	wrongOwner := identity
	wrongOwner.Owner = "agent:other"
	if _, err := NewStore(store.root, store.keyFile, Limits{}).Restore(wrongOwner, policy); err == nil {
		t.Fatal("owner mismatch must fail closed")
	}
	changedPolicy, err := ParsePolicy(map[string]any{"DOCUMENT_ID": map[string]any{"mode": "bind", "targets": []any{"container"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.root, store.keyFile, Limits{}).Restore(identity, changedPolicy); err == nil {
		t.Fatal("policy mismatch must fail closed")
	}
	wrongKeyStore := NewStore(store.root, filepath.Join(filepath.Dir(store.keyFile), "wrong-run-env.key"), Limits{})
	wrongIdentity := identity
	wrongIdentity.RunID = "run-created-with-wrong-key"
	if _, err := wrongKeyStore.New(wrongIdentity, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKeyStore.Restore(identity, policy); err == nil {
		t.Fatal("wrong checkpoint key must fail closed")
	}
	path := store.path(identity)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.root, store.keyFile, Limits{}).Restore(identity, policy); err == nil {
		t.Fatal("tampered checkpoint must fail closed")
	}
	if err := restored.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint remains after destroy: %v", err)
	}
	if _, _, err := restored.Snapshot(TargetHost, policy); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed snapshot error = %v", err)
	}
}

func TestSnapshotAndMutationAreLinearizable(t *testing.T) {
	store, identity, policy := testStore(t)
	state, err := store.New(identity, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationSet, Name: "SESSION_TOKEN", Value: "value-a"}}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for index := 0; index < 500; index++ {
			value := "value-a"
			if index%2 == 0 {
				value = "value-b"
			}
			if _, err := state.Mutate(MutationRequest{Operations: []Mutation{{Operation: OperationSet, Name: "SESSION_TOKEN", Value: value}}}); err != nil {
				t.Errorf("mutate: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for index := 0; index < 500; index++ {
			snapshot, _, err := state.Snapshot(TargetHost, policy)
			if err != nil {
				t.Errorf("snapshot: %v", err)
				return
			}
			if value := snapshot["SESSION_TOKEN"]; value != "value-a" && value != "value-b" {
				t.Errorf("partial value %q", value)
				return
			}
		}
	}()
	wg.Wait()
}

func TestPolicyRejectsReservedNamesInvalidTargetsAndUnsafeValues(t *testing.T) {
	if _, err := ParsePolicy(map[string]any{"Path": map[string]any{"mode": "mutable"}}); err == nil {
		t.Fatal("mixed-case reserved key must fail")
	}
	if _, err := ParsePolicy(map[string]any{"SAFE_KEY": map[string]any{"mode": "mutable", "targets": []any{"mcp"}}}); err == nil {
		t.Fatal("invalid target must fail")
	}
	policy, err := ParsePolicy(map[string]any{"SAFE_KEY": map[string]any{"mode": "mutable", "maxBytes": 8.0}})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := policy.Key("SAFE_KEY")
	for _, value := range []string{"", "line1\nline2", "contains\x00nul", "too-long-value"} {
		if err := item.ValidateValue(value, 4096); err == nil {
			t.Fatalf("unsafe value %q was accepted", value)
		}
	}
}

func testStore(t *testing.T) (*Store, Identity, Policy) {
	t.Helper()
	root := t.TempDir()
	policy, err := ParsePolicy(map[string]any{
		"DOCUMENT_ID":   map[string]any{"mode": "bind", "secret": false, "pattern": `doc-[0-9]{3}`, "maxBytes": 7, "targets": []any{"host", "container"}},
		"SESSION_TOKEN": map[string]any{"mode": "mutable", "secret": true, "targets": []any{"host"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "state"), filepath.Join(root, "identity", "run-env.key"), Limits{})
	return store, Identity{RunID: "run-1", ChatID: "chat-1", Subject: "alice", Owner: "agent:office", AgentKey: "office"}, policy
}
