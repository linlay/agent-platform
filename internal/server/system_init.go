package server

import (
	"reflect"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) ([]chat.QueryLineSystemInit, error) {
	if session == nil || s.deps.Chats == nil || s.deps.Tools == nil {
		return nil, nil
	}
	if s.deps.SystemInits == nil {
		return nil, nil
	}
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		*session,
		req,
		s.deps.Tools.Definitions(),
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	if len(profiles) == 0 {
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
	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	pendingSystems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for index, profile := range profiles {
		initLine := systemInits[profile.CacheKey]
		system := chat.QueryLineSystemInit{
			Fingerprint:    profile.Fingerprint,
			CacheKey:       profile.CacheKey,
			SystemMessage:  cloneMap(profile.SystemMessage),
			Tools:          cloneAnySlice(profile.Tools),
			Model:          cloneMap(profile.Model),
			ToolChoice:     profile.ToolChoice,
			RequestOptions: cloneMap(profile.RequestOptions),
		}
		if initLine != nil && sameSystemInitPayload(initLine, system) {
			cache[profile.CacheKey] = contracts.SystemInitSnapshot{
				Fingerprint:    initLine.Fingerprint,
				SystemMessage:  cloneMap(initLine.SystemMessage),
				Tools:          cloneAnySlice(initLine.Tools),
				Model:          cloneMap(initLine.Model),
				ToolChoice:     initLine.ToolChoice,
				RequestOptions: cloneMap(initLine.RequestOptions),
			}
			continue
		}
		if index != 0 {
			continue
		}
		pendingSystems = append(pendingSystems, system)
		cache[profile.CacheKey] = contracts.SystemInitSnapshot{
			Fingerprint:    profile.Fingerprint,
			SystemMessage:  cloneMap(profile.SystemMessage),
			Tools:          cloneAnySlice(profile.Tools),
			Model:          cloneMap(profile.Model),
			ToolChoice:     profile.ToolChoice,
			RequestOptions: cloneMap(profile.RequestOptions),
		}
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
	profiles := s.deps.SystemInits.BuildSystemInitProfiles(
		*session,
		req,
		s.deps.Tools.Definitions(),
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	systems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for index, profile := range profiles {
		if index > 0 {
			break
		}
		systems = append(systems, chat.QueryLineSystemInit{
			CacheKey:       profile.CacheKey,
			Fingerprint:    profile.Fingerprint,
			SystemMessage:  cloneMap(profile.SystemMessage),
			Tools:          cloneAnySlice(profile.Tools),
			Model:          cloneMap(profile.Model),
			ToolChoice:     profile.ToolChoice,
			RequestOptions: cloneMap(profile.RequestOptions),
		})
	}
	return systems
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
