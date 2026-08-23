package runenv

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestScopeStartsEmptyAndMutatesInMemory(t *testing.T) {
	scope := NewScope(Limits{})
	snapshot, revision, err := scope.Snapshot()
	if err != nil || revision != 0 || len(snapshot) != 0 {
		t.Fatalf("initial snapshot=%#v revision=%d err=%v", snapshot, revision, err)
	}

	result, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: " document_id ", Value: "doc-1"})
	if err != nil || !result.Changed || result.Revision != 1 || result.Key != "DOCUMENT_ID" {
		t.Fatalf("first set = %#v, %v", result, err)
	}
	snapshot, revision, err = scope.Snapshot()
	if err != nil || revision != 1 || snapshot["DOCUMENT_ID"] != "doc-1" {
		t.Fatalf("set snapshot=%#v revision=%d err=%v", snapshot, revision, err)
	}

	unchanged, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-1"})
	if err != nil || unchanged.Changed || unchanged.Revision != 1 {
		t.Fatalf("same-value set = %#v, %v", unchanged, err)
	}

	overwrite, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-2"})
	if err != nil || !overwrite.Changed || overwrite.Revision != 2 {
		t.Fatalf("overwrite = %#v, %v", overwrite, err)
	}
	unset, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID"})
	if err != nil || !unset.Changed || unset.Revision != 3 {
		t.Fatalf("unset = %#v, %v", unset, err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationUnset, Name: "DOCUMENT_ID"}); !errors.Is(err, ErrKeyNotSet) {
		t.Fatalf("repeated unset error = %v", err)
	}
}

func TestScopeExpectedRevisionAndIdempotency(t *testing.T) {
	scope := NewScope(Limits{})
	zero := uint64(0)
	first, err := scope.Mutate(MutationRequest{
		Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-1",
		ExpectedRevision: &zero, IdempotencyKey: "set-document",
	})
	if err != nil || first.Revision != 1 || first.Idempotent {
		t.Fatalf("first idempotent set = %#v, %v", first, err)
	}

	retry, err := scope.Mutate(MutationRequest{
		Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-1",
		ExpectedRevision: &zero, IdempotencyKey: "set-document",
	})
	if err != nil || !retry.Idempotent || retry.Revision != 1 || !retry.Changed {
		t.Fatalf("retried set = %#v, %v", retry, err)
	}
	if _, err := scope.Mutate(MutationRequest{
		Operation: OperationSet, Name: "DOCUMENT_ID", Value: "doc-2", IdempotencyKey: "set-document",
	}); err == nil || !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{
		Operation: OperationSet, Name: "OTHER", Value: "value", ExpectedRevision: &zero,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision conflict = %v", err)
	}

	unset, err := scope.Mutate(MutationRequest{
		Operation: OperationUnset, Name: "DOCUMENT_ID", DefaultIdempotencyKey: "run-1:tool-unset",
	})
	if err != nil || unset.Revision != 2 {
		t.Fatalf("idempotent unset = %#v, %v", unset, err)
	}
	retriedUnset, err := scope.Mutate(MutationRequest{
		Operation: OperationUnset, Name: "DOCUMENT_ID", DefaultIdempotencyKey: "run-1:tool-unset",
	})
	if err != nil || !retriedUnset.Idempotent || retriedUnset.Revision != 2 {
		t.Fatalf("retried unset = %#v, %v", retriedUnset, err)
	}
}

func TestScopeValidationAndLimits(t *testing.T) {
	scope := NewScope(Limits{
		MaxDynamicKeys:  2,
		MaxValueBytes:   4,
		MaxTotalBytes:   6,
		ExtraDeniedKeys: []string{"DENIED"},
	})
	for _, request := range []MutationRequest{
		{Operation: OperationSet, Name: "PATH", Value: "x"},
		{Operation: OperationSet, Name: "DENIED", Value: "x"},
		{Operation: OperationSet, Name: "BAD-NAME", Value: "x"},
		{Operation: OperationSet, Name: "TOO_LONG", Value: "12345"},
		{Operation: OperationSet, Name: "NUL", Value: "a\x00b"},
	} {
		if _, err := scope.Mutate(request); err == nil {
			t.Fatalf("request should fail: %#v", request)
		}
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "FIRST", Value: "1234"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "SECOND", Value: "12"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "THIRD", Value: ""}); err == nil || !strings.Contains(err.Error(), "dynamic keys") {
		t.Fatalf("key limit error = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "SECOND", Value: "123"}); err == nil || !strings.Contains(err.Error(), "total bytes") {
		t.Fatalf("total limit error = %v", err)
	}
}

func TestScopesAreIsolated(t *testing.T) {
	first := NewScope(Limits{})
	second := NewScope(Limits{})
	if _, err := first.Mutate(MutationRequest{Operation: OperationSet, Name: "SHARED", Value: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Mutate(MutationRequest{Operation: OperationUnset, Name: "SHARED"}); !errors.Is(err, ErrKeyNotSet) {
		t.Fatalf("other scope unset error = %v", err)
	}
	snapshot, revision, err := second.Snapshot()
	if err != nil || revision != 0 || len(snapshot) != 0 {
		t.Fatalf("second scope snapshot=%#v revision=%d err=%v", snapshot, revision, err)
	}
}

func TestConcurrentFirstSetIsLinearizable(t *testing.T) {
	scope := NewScope(Limits{})
	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "same"})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, revision, err := scope.Snapshot()
	if err != nil || revision != 1 || snapshot["VALUE"] != "same" {
		t.Fatalf("snapshot=%#v revision=%d err=%v", snapshot, revision, err)
	}
}

func TestDestroyClosesAndClearsScope(t *testing.T) {
	scope := NewScope(Limits{})
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "data"}); err != nil {
		t.Fatal(err)
	}
	scope.Destroy()
	scope.Destroy()
	if scope.Revision() != 0 {
		t.Fatalf("closed revision = %d", scope.Revision())
	}
	if _, _, err := scope.Snapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed snapshot error = %v", err)
	}
	if _, err := scope.Mutate(MutationRequest{Operation: OperationSet, Name: "VALUE", Value: "again"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed mutation error = %v", err)
	}
}
