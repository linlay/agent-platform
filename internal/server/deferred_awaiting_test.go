package server

import (
	"testing"

	"agent-platform/internal/runtime/runstate"
)

func TestServerDeferredAwaitingStoreConstruction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store DeferredAwaitingStore
	}{
		{name: "default"},
		{name: "injected", store: runstate.NewDeferredAwaitingStore()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, err := New(Dependencies{BackgroundContext: t.Context(), DeferredAwaitings: tc.store})
			if err != nil {
				t.Fatal(err)
			}
			store, ok := server.deferredAwaitings.(*runstate.DeferredAwaitingStore)
			if !ok || store == nil || server.deps.DeferredAwaitings != store {
				t.Fatalf("server must share its runtime store with dependencies: %T", server.deferredAwaitings)
			}
			if tc.store != nil && store != tc.store {
				t.Fatal("server replaced the injected awaiting store")
			}
		})
	}
}
