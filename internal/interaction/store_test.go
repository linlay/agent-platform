package interaction

import (
	"errors"
	"sync"
	"testing"
)

func TestImmutablePolicySurvivesRestartAndConcurrentClaim(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := Defaults("REACT")
	b := a
	b.Model = false
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, scope := range []Config{a, b} {
		wg.Add(1)
		go func(scope Config) { defer wg.Done(); results <- store.Bind("../run", scope) }(scope)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("claims: %d/%d", success, conflict)
	}
	restored, err := (Store{Root: store.Root}).Load("../run")
	if err != nil || (restored != a && restored != b) {
		t.Fatalf("restore: %#v %v", restored, err)
	}
	if err := store.Bind("../run", restored); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("missing"); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}
