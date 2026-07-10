package server

import (
	"reflect"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) (*chat.QueryLineSystemInit, error) {
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

func (s *Server) prepareSystemInitCacheFrom(req api.QueryRequest, session *contracts.QuerySession, systemInits chat.SystemInitIndex) (*chat.QueryLineSystemInit, error) {
	if session == nil || s.deps.Tools == nil || s.deps.SystemInits == nil {
		return nil, nil
	}
	toolDefs := s.deps.Tools.Definitions()
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		*session,
		req,
		toolDefs,
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	if len(profiles) == 0 {
		return nil, nil
	}
	if systemInits == nil {
		systemInits = chat.SystemInitIndex{}
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	pendingKeys := make(map[string]bool, len(profiles))
	systemsByCacheKey := make(map[string]chat.QueryLineSystemInit, len(profiles))
	for _, profile := range profiles {
		system := queryLineSystemInitFromProfile(profile)
		systemsByCacheKey[profile.CacheKey] = system
		initLine := systemInits.Lookup(profile.AgentKey, profile.CacheKey)
		if initLine != nil && sameSystemInitPayload(initLine, system) {
			cache[profile.CacheKey] = systemInitSnapshotFromLine(chat.QueryLineSystemInit{
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
	initialCacheKey := initialSystemInitCacheKey(*session)
	var initialSystem *chat.QueryLineSystemInit
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

func sameSystemInitPayload(initLine *chat.SystemInitLine, system chat.QueryLineSystemInit) bool {
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
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		*session,
		req,
		toolDefs,
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	if len(profiles) == 0 {
		return
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	for _, profile := range profiles {
		line := queryLineSystemInitFromProfile(profile)
		cache[line.CacheKey] = systemInitSnapshotFromLine(line)
	}
	session.SystemInitCache = cache
	session.PendingSystemInitKeys = nil
}

func initialSystemInitCacheKey(session contracts.QuerySession) string {
	if agentcoder.PlanningModeEnabled(session.Mode, session.PlanningMode) {
		return "coder:plan"
	}
	if strings.EqualFold(strings.TrimSpace(session.Mode), "PLAN_EXECUTE") || strings.EqualFold(strings.TrimSpace(session.Mode), "PLAN-EXECUTE") {
		return "plan-execute:plan"
	}
	return llmSystemInitCacheKey(session.Mode)
}

func llmSystemInitCacheKey(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "oneshot":
		return "oneshot:main"
	case "coder":
		return "coder:main"
	case "kbase":
		return "kbase:main"
	default:
		return "react:main"
	}
}

func queryLineSystemInitFromProfile(profile contracts.SystemInitProfile) chat.QueryLineSystemInit {
	return chat.QueryLineSystemInit{
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

func systemInitSnapshotFromLine(line chat.QueryLineSystemInit) contracts.SystemInitSnapshot {
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
