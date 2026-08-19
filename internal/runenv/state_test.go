package runenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestScopeIsLazyUntilFirstSuccessfulSet(t *testing.T) {
	store, identity := testStore(t, Limits{})
	scope, err := store.NewScope(identity)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := store.HasCheckpoint(identity); err != nil || exists {
		t.Fatalf("new scope checkpoint exists=%v err=%v", exists, err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "MISSING"}); !errors.Is(err, ErrKeyNotSet) {
		t.Fatalf("missing unset error = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "PATH", Value: "bad"}); err == nil {
		t.Fatal("reserved key must fail")
	}
	if exists, err := store.HasCheckpoint(identity); err != nil || exists || scope.Revision() != 0 {
		t.Fatalf("failed mutation materialized state: exists=%v revision=%d err=%v", exists, scope.Revision(), err)
	}
	result, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-1"})
	if err != nil || !result.Changed || result.Revision != 1 || result.Key != "DOCUMENT_ID" {
		t.Fatalf("first set = %#v, %v", result, err)
	}
	if exists, err := store.HasCheckpoint(identity); err != nil || !exists {
		t.Fatalf("successful set checkpoint exists=%v err=%v", exists, err)
	}
}

func TestFailedFirstSetDoesNotMaterializeStateOrCheckpoint(t *testing.T) {
	store, identity := testStore(t, Limits{MaxValueBytes: 100, MaxTotalBytes: 1})
	scope, _ := store.NewScope(identity)
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "too large"}); err == nil {
		t.Fatal("expected total limit error")
	}
	if scope.state != nil || scope.Revision() != 0 {
		t.Fatalf("failed first set materialized state: %#v", scope.state)
	}
	if exists, err := store.HasCheckpoint(identity); err != nil || exists {
		t.Fatalf("failed first set checkpoint exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(store.keyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first set created checkpoint key: %v", err)
	}
	nonzero := uint64(1)
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "x", ExpectedRevision: &nonzero}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("failed first set revision error = %v", err)
	}
	if scope.state != nil {
		t.Fatalf("revision-conflicted first set materialized state: %#v", scope.state)
	}
	if _, err := os.Stat(store.keyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revision-conflicted first set created checkpoint key: %v", err)
	}
}

func TestSetOverwriteEmptyMultilineAndUnsetSemantics(t *testing.T) {
	store, identity := testStore(t, Limits{})
	scope, _ := store.NewScope(identity)
	set := func(value string, idempotency string) MutationResult {
		t.Helper()
		result, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "DOCUMENT_ID", Value: value, IdempotencyKey: idempotency})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := set("", ""); got.Revision != 1 || !got.Changed {
		t.Fatalf("empty set = %#v", got)
	}
	if got := set("", ""); got.Revision != 1 || got.Changed {
		t.Fatalf("same set = %#v", got)
	}
	if got := set("line 1\nline 2", "set-lines"); got.Revision != 2 || !got.Changed {
		t.Fatalf("multiline set = %#v", got)
	}
	if got := set("line 1\nline 2", "set-lines"); got.Revision != 2 || !got.Changed || !got.Idempotent {
		t.Fatalf("set retry = %#v", got)
	}
	snapshot, revision, err := scope.Snapshot()
	if err != nil || revision != 2 || snapshot["DOCUMENT_ID"] != "line 1\nline 2" {
		t.Fatalf("snapshot=%#v revision=%d err=%v", snapshot, revision, err)
	}
	unset, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID", IdempotencyKey: "unset-doc"})
	if err != nil || !unset.Changed || unset.Revision != 3 {
		t.Fatalf("unset = %#v, %v", unset, err)
	}
	retry, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID", IdempotencyKey: "unset-doc"})
	if err != nil || !retry.Idempotent || retry.Revision != 3 || !retry.Changed {
		t.Fatalf("unset retry = %#v, %v", retry, err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID"}); !errors.Is(err, ErrKeyNotSet) {
		t.Fatalf("repeated unset error = %v", err)
	}
	restored, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(identity, 3)
	if err != nil {
		t.Fatal(err)
	}
	restoredRetry, err := restored.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID", IdempotencyKey: "unset-doc"})
	if err != nil || !restoredRetry.Idempotent || restoredRetry.Revision != 3 {
		t.Fatalf("restored unset retry = %#v, %v", restoredRetry, err)
	}
	if snapshot, _, _ := scope.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("unset left dynamic values: %#v", snapshot)
	}
}

func TestUnsetCannotRemoveAnotherRunValue(t *testing.T) {
	store, firstIdentity := testStore(t, Limits{})
	first, _ := store.NewScope(firstIdentity)
	if _, err := first.Mutate(MutationRequest{Operation: OperationSet, Name: "SHARED", Value: "first"}); err != nil {
		t.Fatal(err)
	}
	secondIdentity := firstIdentity
	secondIdentity.RunID = "run-2"
	second, _ := store.NewScope(secondIdentity)
	if _, err := second.Mutate(MutationRequest{Operation: OperationUnset, Name: "SHARED"}); !errors.Is(err, ErrKeyNotSet) {
		t.Fatalf("other run unset error = %v", err)
	}
	snapshot, _, _ := first.Snapshot()
	if snapshot["SHARED"] != "first" {
		t.Fatalf("other run changed first scope: %#v", snapshot)
	}
}

func TestRevisionIdempotencyLimitsAndValidation(t *testing.T) {
	store, identity := testStore(t, Limits{MaxDynamicKeys: 1, MaxValueBytes: 16, MaxTotalBytes: 8, ExtraDeniedKeys: []string{"DENIED"}})
	scope, _ := store.NewScope(identity)
	expected := uint64(0)
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "FIRST", Value: "12345678", ExpectedRevision: &expected, IdempotencyKey: "first"}); err != nil {
		t.Fatal(err)
	}
	stale := uint64(0)
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "FIRST", Value: "changed", ExpectedRevision: &stale}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "SECOND", Value: "x"}); err == nil || !strings.Contains(err.Error(), "dynamic keys") {
		t.Fatalf("key limit error = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "FIRST", Value: "123456789"}); err == nil || !strings.Contains(err.Error(), "total bytes") {
		t.Fatalf("total limit error = %v", err)
	}
	for name, value := range map[string]string{
		"DENIED":   "x",
		"PATH":     "x",
		"BAD-NAME": "x",
		"NUL":      "x\x00y",
		"UTF8":     string([]byte{0xff}),
	} {
		if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: name, Value: value}); err == nil {
			t.Fatalf("invalid mutation accepted for %s", name)
		}
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "FIRST", Value: "different", IdempotencyKey: "first"}); err == nil || !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("idempotency conflict = %v", err)
	}
}

func TestCheckpointV2RestoreAuthenticationAndCleanup(t *testing.T) {
	store, identity := testStore(t, Limits{})
	scope, _ := store.NewScope(identity)
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-123"}); err != nil {
		t.Fatal(err)
	}
	restored, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, revision, err := restored.Snapshot()
	if err != nil || revision != 1 || snapshot["DOCUMENT_ID"] != "doc-123" {
		t.Fatalf("restored=%#v revision=%d err=%v", snapshot, revision, err)
	}
	wrongOwner := identity
	wrongOwner.Owner = "agent:other"
	if _, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(wrongOwner, 1); err == nil {
		t.Fatal("owner mismatch must fail closed")
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
	if _, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(identity, 1); err == nil {
		t.Fatal("tampered checkpoint must fail closed")
	}
	if err := restored.Destroy(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint remains after destroy: %v", err)
	}
	if _, _, err := restored.Snapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed snapshot error = %v", err)
	}
}

func TestRestoreRevisionZeroMissingAndV1FailClosed(t *testing.T) {
	store, identity := testStore(t, Limits{})
	if scope, err := store.RestoreScope(identity, 0); err != nil || scope.Revision() != 0 {
		t.Fatalf("revision zero restore = %#v, %v", scope, err)
	}
	if _, err := store.RestoreScope(identity, 1); err == nil {
		t.Fatal("missing checkpoint at positive revision must fail")
	}
	key, err := store.loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"version":1,"revision":1,"values":{"A":"Yg=="}}`)
	encrypted, err := encryptCheckpoint(key, payload, aad(identity))
	zero(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(identity), encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(identity, 1); err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Fatalf("v1 restore error = %v", err)
	}
	if _, err := NewStore(store.root, store.keyFile, Limits{}).RestoreScope(identity, 0); err == nil {
		t.Fatal("revision zero with checkpoint must fail closed")
	}
}

func TestConcurrentFirstSetCreatesOneLinearizableState(t *testing.T) {
	store, identity := testStore(t, Limits{})
	scope, _ := store.NewScope(identity)
	var wait sync.WaitGroup
	wait.Add(8)
	for index := 0; index < 8; index++ {
		go func() {
			defer wait.Done()
			if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "same"}); err != nil {
				t.Errorf("set: %v", err)
			}
		}()
	}
	wait.Wait()
	if scope.Revision() != 1 {
		t.Fatalf("revision = %d, want 1", scope.Revision())
	}
}

func testStore(t *testing.T, limits Limits) (*Store, Identity) {
	t.Helper()
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "state"), filepath.Join(root, "identity", "run-env.key"), limits)
	return store, Identity{RunID: "run-1", ChatID: "chat-1", Subject: "alice", Owner: "agent:office", AgentKey: "office"}
}
