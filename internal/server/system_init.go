package server

import (
	"log"
	"reflect"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) (*chat.QueryLineSystem, error) {
	if session == nil || s.deps.Chats == nil || s.deps.Tools == nil {
		return nil, nil
	}
	systemInits := chat.SystemInitIndex{}
	if !created {
		var err error
		systemInits, err = s.deps.Chats.LoadAllSystemInits(req.ChatID)
		if err != nil {
			return nil, err
		}
	}
	return s.prepareSystemInitCacheFrom(req, session, systemInits)
}

func (s *Server) prepareSystemInitCacheFrom(req api.QueryRequest, session *contracts.QuerySession, systemInits chat.SystemInitIndex) (*chat.QueryLineSystem, error) {
	if session == nil || s.deps.Tools == nil || s.deps.SystemInits == nil {
		return nil, nil
	}
	toolDefs := s.deps.Tools.Definitions()
	profiles, err := s.deps.SystemInits.BuildSystemInitProfiles(contracts.SystemInitBuildInput{
		Session: *session, Request: req, ToolDefinitions: toolDefs,
	})
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	if systemInits == nil {
		systemInits = chat.SystemInitIndex{}
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	pendingKeys := make(map[string]bool, len(profiles))
	systemsByCacheKey := make(map[string]chat.QueryLineSystem, len(profiles))
	initialCacheKey := ""
	for _, profile := range profiles {
		if profile.Initial {
			initialCacheKey = profile.CacheKey
		}
		system := queryLineSystemFromProfile(profile)
		sanitizeTeamCoordinatorSystemInit(session, &system)
		systemsByCacheKey[profile.CacheKey] = system
		initLine := systemInits.Lookup(system.AgentKey, profile.CacheKey)
		if initLine != nil && sameSystemInitPayload(initLine, system) {
			cache[profile.CacheKey] = systemInitSnapshotFromLine(chat.QueryLineSystem{
				AgentKey:       initLine.AgentKey,
				Fingerprint:    initLine.Fingerprint,
				CacheKey:       initLine.CacheKey,
				SystemMessage:  cloneMap(initLine.SystemMessage),
				Tools:          cloneAnySlice(initLine.Tools),
				Model:          cloneMap(initLine.Model),
				ToolChoice:     initLine.ToolChoice,
				RequestOptions: cloneMap(initLine.RequestOptions),
			})
			continue
		}
		pendingKeys[profile.CacheKey] = true
		cache[profile.CacheKey] = systemInitSnapshotFromLine(system)
	}
	if len(cache) > 0 {
		session.SystemInitCache = cache
	}
	var initialSystem *chat.QueryLineSystem
	if pendingKeys[initialCacheKey] {
		system := systemsByCacheKey[initialCacheKey]
		initialSystem = &system
		delete(pendingKeys, initialCacheKey)
	}
	if len(pendingKeys) > 0 {
		session.PendingSystemInitKeys = pendingKeys
	} else {
		session.PendingSystemInitKeys = nil
	}
	return initialSystem, nil
}

func sameSystemInitPayload(initLine *chat.SystemInitLine, system chat.QueryLineSystem) bool {
	if initLine == nil {
		return false
	}
	return initLine.Fingerprint == system.Fingerprint &&
		strings.TrimSpace(initLine.AgentKey) == strings.TrimSpace(system.AgentKey) &&
		reflect.DeepEqual(initLine.SystemMessage, system.SystemMessage) &&
		reflect.DeepEqual(initLine.Tools, system.Tools) &&
		reflect.DeepEqual(initLine.Model, system.Model) &&
		initLine.ToolChoice == system.ToolChoice &&
		reflect.DeepEqual(initLine.RequestOptions, system.RequestOptions)
}

func (s *Server) hydrateSystemInitCache(req api.QueryRequest, session *contracts.QuerySession) {
	if session == nil || s.deps.SystemInits == nil {
		return
	}
	var toolDefs []api.ToolDetailResponse
	if s.deps.Tools != nil {
		toolDefs = s.deps.Tools.Definitions()
	}
	profiles, err := s.deps.SystemInits.BuildSystemInitProfiles(contracts.SystemInitBuildInput{
		Session: *session, Request: req, ToolDefinitions: toolDefs,
	})
	if err != nil {
		log.Printf("[server][system-init] hydrate profiles failed chatId=%s agentKey=%s: %v", req.ChatID, session.AgentKey, err)
		return
	}
	if len(profiles) == 0 {
		return
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	for _, profile := range profiles {
		line := queryLineSystemFromProfile(profile)
		sanitizeTeamCoordinatorSystemInit(session, &line)
		cache[line.CacheKey] = systemInitSnapshotFromLine(line)
	}
	session.SystemInitCache = cache
	session.PendingSystemInitKeys = nil
}

// The coordinator's AgentKey exists only inside the run so the model and
// sandbox code can use the ordinary Agent contract. Persisted system-init
// records use a stable public Team-scoped key instead, never the synthetic
// execution key.
func sanitizeTeamCoordinatorSystemInit(session *contracts.QuerySession, line *chat.QueryLineSystem) {
	if session == nil || line == nil || session.TeamRuntime == nil {
		return
	}
	teamID := strings.TrimSpace(session.TeamID)
	if teamID == "" {
		return
	}
	line.AgentKey = "team:" + teamID
}

func queryLineSystemFromProfile(profile contracts.SystemInitProfile) chat.QueryLineSystem {
	return chat.QueryLineSystem{
		AgentKey:       strings.TrimSpace(profile.AgentKey),
		Fingerprint:    profile.Fingerprint,
		CacheKey:       profile.CacheKey,
		SystemMessage:  cloneMap(profile.SystemMessage),
		Tools:          cloneAnySlice(profile.Tools),
		Model:          cloneMap(profile.Model),
		ToolChoice:     profile.ToolChoice,
		RequestOptions: cloneMap(profile.RequestOptions),
	}
}

func systemInitSnapshotFromLine(line chat.QueryLineSystem) contracts.SystemInitSnapshot {
	return contracts.SystemInitSnapshot{
		AgentKey:       strings.TrimSpace(line.AgentKey),
		Fingerprint:    line.Fingerprint,
		SystemMessage:  cloneMap(line.SystemMessage),
		Tools:          cloneAnySlice(line.Tools),
		Model:          cloneMap(line.Model),
		ToolChoice:     line.ToolChoice,
		RequestOptions: cloneMap(line.RequestOptions),
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneAnySlice(src []any) []any {
	if src == nil {
		return nil
	}
	return append([]any(nil), src...)
}
