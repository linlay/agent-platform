package session

import (
	"reflect"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Builder) PrepareSystemInitCache(req runtimetypes.QueryCommand, session *contracts.QuerySession, created bool) (*chat.QueryLineSystem, error) {
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
	return s.PrepareSystemInitCacheFrom(req, session, systemInits)
}

func (s *Builder) PrepareSystemInitCacheFrom(req runtimetypes.QueryCommand, session *contracts.QuerySession, systemInits chat.SystemInitIndex) (*chat.QueryLineSystem, error) {
	if session == nil || s.deps.Tools == nil || s.deps.Profiles == nil {
		return nil, nil
	}
	profiles, err := s.deps.Profiles.Profiles(req, *session)
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
		system := QueryLineSystemFromProfile(profile)
		SanitizeTeamCoordinatorSystemInit(session, &system)
		systemsByCacheKey[profile.CacheKey] = system
		initLine := systemInits.Lookup(system.AgentKey, profile.CacheKey)
		if initLine != nil && SameSystemInitPayload(initLine, system) {
			cache[profile.CacheKey] = SystemInitSnapshotFromLine(chat.QueryLineSystem{
				AgentKey:       initLine.AgentKey,
				Fingerprint:    initLine.Fingerprint,
				CacheKey:       initLine.CacheKey,
				SystemMessage:  CloneMap(initLine.SystemMessage),
				Tools:          CloneAnySlice(initLine.Tools),
				Model:          CloneMap(initLine.Model),
				ToolChoice:     initLine.ToolChoice,
				RequestOptions: CloneMap(initLine.RequestOptions),
			})
			continue
		}
		pendingKeys[profile.CacheKey] = true
		cache[profile.CacheKey] = SystemInitSnapshotFromLine(system)
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

func SameSystemInitPayload(initLine *chat.SystemInitLine, system chat.QueryLineSystem) bool {
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

// The coordinator's AgentKey exists only inside the run so the model and
// sandbox code can use the ordinary Agent contract. Persisted system-init
// records use a stable public Team-scoped key instead, never the synthetic
// execution key.
func SanitizeTeamCoordinatorSystemInit(session *contracts.QuerySession, line *chat.QueryLineSystem) {
	if session == nil || line == nil || session.TeamRuntime == nil {
		return
	}
	teamID := strings.TrimSpace(session.TeamID)
	if teamID == "" {
		return
	}
	line.AgentKey = "team:" + teamID
}

func QueryLineSystemFromProfile(profile contracts.SystemInitProfile) chat.QueryLineSystem {
	return chat.QueryLineSystem{
		AgentKey:       strings.TrimSpace(profile.AgentKey),
		Fingerprint:    profile.Fingerprint,
		CacheKey:       profile.CacheKey,
		SystemMessage:  CloneMap(profile.SystemMessage),
		Tools:          CloneAnySlice(profile.Tools),
		Model:          CloneMap(profile.Model),
		ToolChoice:     profile.ToolChoice,
		RequestOptions: CloneMap(profile.RequestOptions),
	}
}

func SystemInitSnapshotFromLine(line chat.QueryLineSystem) contracts.SystemInitSnapshot {
	return contracts.SystemInitSnapshot{
		AgentKey:       strings.TrimSpace(line.AgentKey),
		Fingerprint:    line.Fingerprint,
		SystemMessage:  CloneMap(line.SystemMessage),
		Tools:          CloneAnySlice(line.Tools),
		Model:          CloneMap(line.Model),
		ToolChoice:     line.ToolChoice,
		RequestOptions: CloneMap(line.RequestOptions),
	}
}

func CloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func CloneAnySlice(src []any) []any {
	if src == nil {
		return nil
	}
	return append([]any(nil), src...)
}
