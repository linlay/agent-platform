package server

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"agent-platform/internal/runtime/adapter"
	"agent-platform/internal/runtime/session"
)

func (s *Server) buildRuntimeRequestContext(input runtimeRequestContextInput) (contracts.RuntimeRequestContext, error) {
	return testSessionBuilder(s).BuildContext(input)
}
func (s *Server) configureSessionViews(input *contracts.QuerySession, def catalog.AgentDefinition) error {
	return testSessionBuilder(s).ConfigureViews(input, def)
}
func (s *Server) normalizeReferencePathsForAgent(refs []api.Reference, chatID string, def catalog.AgentDefinition, paths contracts.LocalPaths) ([]api.Reference, error) {
	return testSessionBuilder(s).NormalizeReferences(refs, chatID, def, paths)
}

func testSessionBuilder(s *Server) *session.Builder {
	d := s.deps
	return session.New(session.Dependencies{Config: d.Config, Chats: d.Chats, Registry: adapter.Catalog{Registry: d.Registry}, Models: d.Models, Runs: d.Runs, Tools: d.Tools, Profiles: adapter.Profiles{Builder: d.SystemInits, Tools: d.Tools}})
}
func (s *Server) BuildQuerySession(ctx context.Context, req api.QueryRequest, summary chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	s.deps.Sessions = testSessionBuilder(s)
	return s.buildCompactSession(ctx, req, summary, def, options)
}

func (s *Server) buildCurrentMessages(req api.QueryRequest, input contracts.QuerySession) []map[string]any {
	return testSessionBuilder(s).BuildCurrentMessages(queryCommandFromAPI(req), input)
}

func (s *Server) resolveQueryMustUseSkills(def catalog.AgentDefinition, requested []string) (mustUseSkillResolution, error) {
	return testSessionBuilder(s).ResolveSkills(def, requested)
}

func (s *Server) newRunEnvironmentScope() *runenv.Scope {
	return testSessionBuilder(s).NewRunEnvironmentScope()
}

func resolveMustUseSkills(def catalog.AgentDefinition, dir string, registry legacySkillCatalog, requested []string) (mustUseSkillResolution, error) {
	return session.ResolveMustUseSkills(def, dir, skillCatalogAdapter{registry}, requested)
}

func buildAgentDigests(registry catalog.Registry) []contracts.AgentDigest {
	return session.BuildAgentDigests(adapter.Catalog{Registry: registry})
}

func buildContextAgentDigests(registry catalog.Registry, def catalog.AgentDefinition, key string) ([]contracts.AgentDigest, *catalog.AdminAgentDiagnostic) {
	return session.BuildContextAgentDigests(adapter.Catalog{Registry: registry}, def, key)
}

type legacySkillCatalog interface {
	Skills(string) []api.SkillSummary
	SkillDefinition(string) (catalog.SkillDefinition, bool)
}

type skillCatalogAdapter struct{ legacySkillCatalog }

func (c skillCatalogAdapter) SkillIDs() []string {
	if c.legacySkillCatalog == nil {
		return nil
	}
	var keys []string
	for _, s := range c.Skills("") {
		keys = append(keys, s.ID)
	}
	return keys
}

func (s *Server) prepareSystemInitCache(req api.QueryRequest, input *contracts.QuerySession, created bool) (*chat.QueryLineSystem, error) {
	return testSessionBuilder(s).PrepareSystemInitCache(queryCommandFromAPI(req), input, created)
}

func (s *Server) prepareSystemInitCacheFrom(req api.QueryRequest, input *contracts.QuerySession, index chat.SystemInitIndex) (*chat.QueryLineSystem, error) {
	return testSessionBuilder(s).PrepareSystemInitCacheFrom(queryCommandFromAPI(req), input, index)
}
