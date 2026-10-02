package runenv

import (
	"errors"
	"fmt"
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

func TestAtomicUpdateFinalLimitsAndRollback(t *testing.T) {
	s := NewScope(Limits{MaxDynamicKeys: 1, MaxTotalBytes: 4})
	if _, err := s.Mutate(MutationRequest{Operation: OperationSet, Name: "A", Value: "1234"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Mutate(MutationRequest{Operation: OperationUpdate, Set: map[string]string{" b ": "新"}, Unset: []string{"a"}})
	if err != nil || r.Revision != 2 {
		t.Fatalf("replace: %#v %v", r, err)
	}
	for _, req := range []MutationRequest{
		{Operation: OperationUpdate, Set: map[string]string{"B": "x"}, Unset: []string{"MISSING"}},
		{Operation: OperationUpdate, Set: map[string]string{"b": "x", " B ": "y"}},
		{Operation: OperationUpdate, Set: map[string]string{"b": "x"}, Unset: []string{"B"}},
		{Operation: OperationUpdate, Unset: []string{"b", " B "}},
		{Operation: OperationUpdate, Set: map[string]string{"PATH": "x"}},
		{Operation: OperationUpdate, Set: map[string]string{"C": "xx"}},
		{Operation: OperationUpdate},
	} {
		if _, err := s.Mutate(req); err == nil {
			t.Fatalf("accepted %#v", req)
		}
	}
	view, err := s.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if view["revision"] != uint64(2) || view["variables"].(map[string]string)["B"] != "新" || view["usage"].(map[string]any)["totalBytes"] != 3 {
		t.Fatalf("bad view %#v", view)
	}
	r, err = s.Mutate(MutationRequest{Operation: OperationUpdate, Set: map[string]string{"b": "新"}})
	if err != nil || r.Changed || r.Revision != 2 {
		t.Fatalf("noop %#v %v", r, err)
	}
}

func TestIdempotencyDigestRevisionAndCapacity(t *testing.T) {
	s := NewScope(Limits{})
	zero := uint64(0)
	req := MutationRequest{Operation: OperationUpdate, Set: map[string]string{" B ": "two", "a": "one"}, ExpectedRevision: &zero, IdempotencyKey: "first"}
	first, err := s.Mutate(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Set = map[string]string{"A": "one", "B": "two"}
	if r, err := s.Mutate(req); err != nil || !r.Idempotent || r.Revision != first.Revision {
		t.Fatalf("retry %#v %v", r, err)
	}
	one := uint64(1)
	req.ExpectedRevision = &one
	if _, err := s.Mutate(req); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("revision changed: %v", err)
	}
	// Fill receipts directly to keep this capacity boundary test small.
	for i := len(s.idempotency); i < MaxIdempotencyRecords; i++ {
		s.idempotency[fmt.Sprint(i)] = storedIdempotency{}
	}
	if _, err := s.Mutate(MutationRequest{Operation: OperationSet, Name: "C", Value: "x", IdempotencyKey: "new"}); !errors.Is(err, ErrIdempotencyLimit) {
		t.Fatalf("capacity %v", err)
	}
	req.ExpectedRevision = &zero
	if r, err := s.Mutate(req); err != nil || !r.Idempotent {
		t.Fatalf("retry at capacity %#v %v", r, err)
	}
	v, _, _ := s.Snapshot()
	if len(v) != 2 {
		t.Fatalf("capacity mutation leaked: %#v", v)
	}
}

func TestConcurrentUpdatesWithSameRevisionHaveOneWinner(t *testing.T) {
	s := NewScope(Limits{})
	zero := uint64(0)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Mutate(MutationRequest{Operation: OperationUpdate, Set: map[string]string{"VALUE": fmt.Sprint(i)}, ExpectedRevision: &zero})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrRevisionConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 || s.Revision() != 1 {
		t.Fatalf("success=%d revision=%d", success, s.Revision())
	}
}

func TestSingleKeyRetriesIncludeExpectedRevisionInDigest(t *testing.T) {
	for _, operation := range []Operation{OperationSet, OperationUnset} {
		t.Run(string(operation), func(t *testing.T) {
			s := NewScope(Limits{})
			if operation == OperationUnset {
				if _, err := s.Mutate(MutationRequest{Operation: OperationSet, Name: "A", Value: "x"}); err != nil {
					t.Fatal(err)
				}
			}
			revision := s.Revision()
			req := MutationRequest{Operation: operation, Name: "A", Value: "", ExpectedRevision: &revision, IdempotencyKey: "retry"}
			first, err := s.Mutate(req)
			if err != nil {
				t.Fatal(err)
			}
			if r, err := s.Mutate(req); err != nil || !r.Idempotent || r.Revision != first.Revision {
				t.Fatalf("original retry %#v %v", r, err)
			}
			newer := s.Revision()
			req.ExpectedRevision = &newer
			if _, err := s.Mutate(req); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed expectedRevision: %v", err)
			}
			req.ExpectedRevision = nil
			if _, err := s.Mutate(req); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("omitted expectedRevision: %v", err)
			}
		})
	}
}
