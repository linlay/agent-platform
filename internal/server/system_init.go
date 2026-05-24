package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) ([]chat.QueryLineSystemInit, error) {
	if session == nil || s.deps.Chats == nil || s.deps.Tools == nil {
		return nil, nil
	}
	profiles := llm.BuildSystemInitProfiles(
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
	if !created && len(systemInits) == 0 {
		session.SystemInitLegacy = true
		return nil, nil
	}

	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	pendingSystems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for _, profile := range profiles {
		initLine := systemInits[profile.CacheKey]
		if initLine != nil && initLine.Fingerprint == profile.Fingerprint {
			cache[profile.CacheKey] = contracts.SystemInitSnapshot{
				Fingerprint:   initLine.Fingerprint,
				SystemMessage: cloneMap(profile.SystemMessage),
				Tools:         cloneAnySlice(initLine.Tools),
			}
			continue
		}
		system := chat.QueryLineSystemInit{
			Fingerprint:   profile.Fingerprint,
			CacheKey:      profile.CacheKey,
			SystemMessage: cloneMap(profile.SystemMessage),
			Tools:         cloneAnySlice(profile.Tools),
		}
		pendingSystems = append(pendingSystems, system)
		cache[profile.CacheKey] = contracts.SystemInitSnapshot{
			Fingerprint:   profile.Fingerprint,
			SystemMessage: cloneMap(profile.SystemMessage),
			Tools:         cloneAnySlice(profile.Tools),
		}
	}
	if len(cache) > 0 {
		session.SystemInitCache = cache
	}
	return pendingSystems, nil
}

func (s *Server) buildSystemInitsForChildTask(req api.QueryRequest, session *contracts.QuerySession) []chat.QueryLineSystemInit {
	if session == nil || s.deps.Tools == nil {
		return nil
	}
	profiles := llm.BuildSystemInitProfiles(
		*session,
		req,
		s.deps.Tools.Definitions(),
		s.deps.Config.Defaults.Plan.MaxSteps,
		s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask,
		s.deps.Config.Prompts,
	)
	systems := make([]chat.QueryLineSystemInit, 0, len(profiles))
	for _, profile := range profiles {
		systems = append(systems, chat.QueryLineSystemInit{
			CacheKey:      profile.CacheKey,
			Fingerprint:   profile.Fingerprint,
			SystemMessage: cloneMap(profile.SystemMessage),
			Tools:         cloneAnySlice(profile.Tools),
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
