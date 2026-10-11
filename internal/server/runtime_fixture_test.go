package server

import (
	"fmt"
	"sync"

	agentruntime "agent-platform/internal/runtime"
	"agent-platform/internal/runtime/adapter"
	"agent-platform/internal/runtime/query"
	"agent-platform/internal/runtime/session"
)

var testQueryServices sync.Map

func newRuntimeServer(deps Dependencies) (*Server, error) {
	facade := agentruntime.NewService()
	deps.Runtime = facade
	s, err := New(deps)
	if err != nil {
		return nil, err
	}
	app := bindTestRuntime(s)
	if err := app.Reconcile(); err != nil {
		return nil, fmt.Errorf("reconcile persisted awaitings: %w", err)
	}
	return s, nil
}

// Fixtures use the same native application components as app.New. Rebinding is
// explicit when a test swaps a store, engine or RunManager to simulate failure.
func bindTestRuntime(s *Server) *query.Service {
	d := s.deps
	profiles := adapter.Profiles{Builder: d.SystemInits, Tools: d.Tools}
	sessions := session.New(session.Dependencies{KnowledgeCollections: testKnowledgeCollections(d), Config: d.Config, Chats: d.Chats, Registry: adapter.Catalog{Registry: d.Registry}, Models: d.Models, Runs: d.Runs, Tools: d.Tools, Profiles: profiles})
	s.deps.Sessions = sessions
	app := query.NewService(query.Dependencies{BackgroundContext: s.backgroundCtx, Config: d.Config, Runs: d.Runs, Chats: d.Chats, Registry: d.Registry, Models: d.Models, Tools: d.Tools, Agent: adapter.Engine{AgentEngine: d.Agent}, Profiles: profiles, Sessions: sessions, Notifications: d.Notifications, ToolInteractions: d.ToolInteractions, DeltaMappers: d.DeltaMappers, DeferredAwaitings: s.deferredAwaitings, Proxy: RuntimeProxyPort{Server: s}, ResourceTickets: s.ticketService})
	facade, ok := s.deps.Runtime.(*agentruntime.Service)
	if !ok {
		facade = agentruntime.NewService()
		s.deps.Runtime = facade
	}
	facade.Bind(app)
	testQueryServices.Store(s, app)
	return app
}
func testQueryService(s *Server) *query.Service {
	if v, ok := testQueryServices.Load(s); ok {
		return v.(*query.Service)
	}
	return bindTestRuntime(s)
}
