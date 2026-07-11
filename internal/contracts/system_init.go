package contracts

import "strings"

// TakePendingSystemInitPayload validates and consumes one pending profile.
// Invalid snapshots remain pending so the normal cache validation path can
// surface the configuration error instead of silently discarding it.
func TakePendingSystemInitPayload(session *QuerySession, cacheKey string) map[string]any {
	cacheKey = strings.TrimSpace(cacheKey)
	if session == nil || cacheKey == "" || !session.PendingSystemInitKeys[cacheKey] {
		return nil
	}
	snapshot, ok := session.SystemInitCache[cacheKey]
	if !ok || strings.TrimSpace(snapshot.Fingerprint) == "" || strings.TrimSpace(snapshot.AgentKey) == "" {
		return nil
	}
	delete(session.PendingSystemInitKeys, cacheKey)
	return map[string]any{
		"agentKey":       snapshot.AgentKey,
		"cacheKey":       cacheKey,
		"fingerprint":    snapshot.Fingerprint,
		"systemMessage":  cloneSystemInitMap(snapshot.SystemMessage),
		"tools":          cloneSystemInitValues(snapshot.Tools),
		"model":          cloneSystemInitMap(snapshot.Model),
		"toolChoice":     snapshot.ToolChoice,
		"requestOptions": cloneSystemInitMap(snapshot.RequestOptions),
	}
}

func cloneSystemInitValues(values []any) []any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, cloneSystemInitValue(value))
	}
	return out
}

func cloneSystemInitMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneSystemInitValue(value)
	}
	return out
}

func cloneSystemInitValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSystemInitMap(typed)
	case []any:
		return cloneSystemInitValues(typed)
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneSystemInitMap(item))
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
