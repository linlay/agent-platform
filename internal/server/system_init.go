package server

import (
	"reflect"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) ([]chat.QueryLineSystemInit, error) {
	if session == nil || s.deps.Chats == nil || s.deps.Tools == nil {
		return nil, nil
	}
	systemInits := map[string]*chat.SystemInitLine{}
	if !created {
		var err error
		systemInits, err = s.deps.Chats.LoadAllSystemInits(req.ChatID)
		if err != nil {
			return nil, err
		}
	}
	return s.prepareSystemInitCacheFrom(req, session, systemInits)
}

func (s *Server) prepareSystemInitCacheFrom(req api.QueryRequest, session *contracts.QuerySession, systemInits map[string]*chat.SystemInitLine) ([]chat.QueryLineSystemInit, error) {
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
	profiles = systemInitProfilesForQueryRegistration(*session, profiles)
	if systemInits == nil {
		systemInits = map[string]*chat.SystemInitLine{}
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	pendingSystems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for _, profile := range profiles {
		initLine := systemInits[profile.CacheKey]
		system := queryLineSystemInitFromProfile(profile)
		if initLine != nil && sameSystemInitPayload(initLine, system) {
			cache[profile.CacheKey] = systemInitSnapshotFromLine(chat.QueryLineSystemInit{
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
		pendingSystems = append(pendingSystems, system)
		cache[profile.CacheKey] = systemInitSnapshotFromLine(system)
	}
	if len(cache) > 0 {
		session.SystemInitCache = cache
	}
	return pendingSystems, nil
}

func sameSystemInitPayload(initLine *chat.SystemInitLine, system chat.QueryLineSystemInit) bool {
	if initLine == nil {
		return false
	}
	return initLine.Fingerprint == system.Fingerprint &&
		reflect.DeepEqual(initLine.SystemMessage, system.SystemMessage) &&
		reflect.DeepEqual(initLine.Tools, system.Tools) &&
		reflect.DeepEqual(initLine.Model, system.Model) &&
		initLine.ToolChoice == system.ToolChoice &&
		reflect.DeepEqual(initLine.RequestOptions, system.RequestOptions)
}

func (s *Server) buildSystemInitsForChildTask(req api.QueryRequest, session *contracts.QuerySession) []chat.QueryLineSystemInit {
	if session == nil || s.deps.Tools == nil {
		return nil
	}
	if s.deps.SystemInits == nil {
		return nil
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
	systems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for _, profile := range systemInitProfilesForQueryRegistration(*session, profiles) {
		systems = append(systems, queryLineSystemInitFromProfile(profile))
	}
	return systems
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
	profiles = systemInitProfilesForQueryRegistration(*session, profiles)
	if len(profiles) == 0 {
		return
	}
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	for _, profile := range profiles {
		line := queryLineSystemInitFromProfile(profile)
		cache[line.CacheKey] = systemInitSnapshotFromLine(line)
	}
	session.SystemInitCache = cache
}

func systemInitProfilesForQueryRegistration(session contracts.QuerySession, profiles []contracts.SystemInitProfile) []contracts.SystemInitProfile {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]contracts.SystemInitProfile, 0, len(profiles))
	for _, profile := range profiles {
		if !shouldRegisterSystemInitProfileOnQuery(session, profile) {
			continue
		}
		out = append(out, profile)
	}
	return out
}

func shouldRegisterSystemInitProfileOnQuery(session contracts.QuerySession, profile contracts.SystemInitProfile) bool {
	if agentcoder.PlanningModeEnabled(session.Mode, session.PlanningMode) {
		return strings.TrimSpace(profile.CacheKey) == "coder:plan"
	}
	return true
}

func queryLineSystemInitFromProfile(profile contracts.SystemInitProfile) chat.QueryLineSystemInit {
	return chat.QueryLineSystemInit{
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
