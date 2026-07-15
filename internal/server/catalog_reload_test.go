package server

import (
	"context"
	"testing"
)

type recordingServerCatalogReloader struct {
	reasons []string
}

func (r *recordingServerCatalogReloader) Reload(_ context.Context, reason string) error {
	r.reasons = append(r.reasons, reason)
	return nil
}

type recordingAgentCardRefresh struct {
	calls int
}

func (r *recordingAgentCardRefresh) ScheduleRefresh() {
	r.calls++
}

func TestReloadAgentCatalogUsesRuntimeReloaderAndSchedulesCards(t *testing.T) {
	reloader := &recordingServerCatalogReloader{}
	refresh := &recordingAgentCardRefresh{}
	server := &Server{deps: Dependencies{
		CatalogReloader:  reloader,
		AgentCardRefresh: refresh,
	}}

	if err := server.reloadAgentCatalog(context.Background()); err != nil {
		t.Fatalf("reload agent catalog: %v", err)
	}
	if len(reloader.reasons) != 1 || reloader.reasons[0] != "agents" {
		t.Fatalf("reload reasons = %#v, want agents", reloader.reasons)
	}
	if refresh.calls != 1 {
		t.Fatalf("card refresh calls = %d, want 1", refresh.calls)
	}
}
