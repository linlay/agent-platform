package controlscope

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestImmutableScopeSurvivesRestartAndConcurrentClaim(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a := Scope{Transport: "ws", Lane: "btw", Subject: "user", Boundary: "device"}
	b := a
	b.Lane = "explain"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, scope := range []Scope{a, b} {
		wg.Add(1)
		go func(scope Scope) { defer wg.Done(); results <- store.Bind("../run", scope) }(scope)
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
func TestControlScopeMatrix(t *testing.T) {
	for _, transport := range []string{"http", "ws"} {
		for _, lane := range []string{"main", "btw", "explain"} {
			owner := Scope{Transport: transport, Lane: lane, Subject: "user", Boundary: "device"}
			for _, callerTransport := range []string{"http", "ws"} {
				for _, callerLane := range []string{"main", "btw", "explain"} {
					caller := owner
					caller.Transport = callerTransport
					caller.Lane = callerLane
					allowed := transport == callerTransport && (transport == "http" || lane == callerLane)
					if (Check(owner, caller) == "") != allowed {
						t.Fatalf("owner=%#v caller=%#v", owner, caller)
					}
				}
			}
			other := owner
			other.Subject = "another"
			if Check(owner, other) == "" {
				t.Fatal("subject bypass")
			}
			other = owner
			other.Boundary = "another"
			if Check(owner, other) == "" {
				t.Fatal("device bypass")
			}
		}
	}
}

func TestInProcessCreationHasNoNetworkTransport(t *testing.T) {
	scope := FromContext(context.Background())
	if scope.Transport != "" || scope.Lane != "" {
		t.Fatalf("unexpected network origin: %#v", scope)
	}
	store := Store{Root: t.TempDir()}
	if err := store.Bind("in-process", scope); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("in-process")
	if err != nil || got != scope {
		t.Fatalf("round trip: %#v %v", got, err)
	}
}
