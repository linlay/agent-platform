package chat

import (
	"encoding/json"
	"os"
	"strings"
)

type SystemInitKey struct {
	AgentKey string
	CacheKey string
}

type SystemInitIndex map[SystemInitKey]*SystemInitLine

func (index SystemInitIndex) Lookup(agentKey string, cacheKey string) *SystemInitLine {
	key := SystemInitKey{AgentKey: strings.TrimSpace(agentKey), CacheKey: strings.TrimSpace(cacheKey)}
	if line := index[key]; line != nil {
		return line
	}
	if key.AgentKey != "" {
		return index[SystemInitKey{CacheKey: key.CacheKey}]
	}
	return nil
}

func (s *FileStore) LoadSystemInit(chatID string, key SystemInitKey) (*SystemInitLine, error) {
	inits, latest, err := s.loadSystemInits(chatID)
	if err != nil {
		return nil, err
	}
	key.AgentKey = strings.TrimSpace(key.AgentKey)
	key.CacheKey = strings.TrimSpace(key.CacheKey)
	if key.CacheKey == "" {
		return latest, nil
	}
	return inits.Lookup(key.AgentKey, key.CacheKey), nil
}

func (s *FileStore) LoadAllSystemInits(chatID string) (SystemInitIndex, error) {
	inits, _, err := s.loadSystemInits(chatID)
	return inits, err
}

func (s *FileStore) loadSystemInits(chatID string) (SystemInitIndex, *SystemInitLine, error) {
	return loadSystemInitsFromPath(s.chatJSONLPath(chatID))
}

func loadSystemInitsFromPath(path string) (SystemInitIndex, *SystemInitLine, error) {
	lines, err := readPersistedJSONLines(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SystemInitIndex{}, nil, nil
		}
		return nil, nil, err
	}
	byKey := SystemInitIndex{}
	var latest *SystemInitLine
	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		switch lineType {
		case "query":
			systems, err := queryLineSystemInitsFromJSONL(line)
			if err != nil {
				return nil, nil, err
			}
			if len(systems) == 0 {
				continue
			}
			for _, parsed := range systems {
				cacheKey := strings.TrimSpace(parsed.CacheKey)
				if cacheKey == "" {
					continue
				}
				mode, stage := parseCacheKey(cacheKey)
				converted := SystemInitLine{
					Type:           "system",
					ChatID:         stringFromAny(line["chatId"]),
					AgentKey:       parsed.AgentKey,
					RunID:          stringFromAny(line["runId"]),
					CreatedAt:      int64FromAny(line["updatedAt"]),
					Fingerprint:    parsed.Fingerprint,
					CacheKey:       parsed.CacheKey,
					Mode:           mode,
					Stage:          stage,
					SystemMessage:  parsed.SystemMessage,
					Tools:          parsed.Tools,
					Model:          parsed.Model,
					ToolChoice:     parsed.ToolChoice,
					RequestOptions: parsed.RequestOptions,
				}
				convertedCopy := converted
				latest = &convertedCopy
				byKey[SystemInitKey{AgentKey: strings.TrimSpace(parsed.AgentKey), CacheKey: cacheKey}] = &convertedCopy
			}
		}
	}
	return byKey, latest, nil
}

func queryLineSystemInitsFromJSONL(line map[string]any) ([]QueryLineSystemInit, error) {
	if len(line) == 0 {
		return nil, nil
	}
	query, _ := line["query"].(map[string]any)
	fallbackAgentKey := firstNonEmptyString(
		stringFromAny(line["subAgentKey"]),
		stringFromAny(line["agentKey"]),
		stringFromAny(query["agentKey"]),
	)
	entries := make([]QueryLineSystemInit, 0, 2)
	positions := map[string]int{}
	appendEntry := func(rawSystem any) error {
		systemMap, _ := rawSystem.(map[string]any)
		if len(systemMap) == 0 {
			return nil
		}
		raw, err := json.Marshal(systemMap)
		if err != nil {
			return err
		}
		var parsed QueryLineSystemInit
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil
		}
		parsed.AgentKey = strings.TrimSpace(firstNonEmptyString(parsed.AgentKey, stringFromAny(systemMap["agentKey"]), fallbackAgentKey))
		parsed.CacheKey = strings.TrimSpace(parsed.CacheKey)
		parsed.Fingerprint = strings.TrimSpace(parsed.Fingerprint)
		if parsed.CacheKey == "" || parsed.Fingerprint == "" {
			return nil
		}
		identity := systemInitRecordID(parsed.AgentKey, parsed.CacheKey, parsed.Fingerprint)
		if index, ok := positions[identity]; ok {
			entries[index] = parsed
			return nil
		}
		positions[identity] = len(entries)
		entries = append(entries, parsed)
		return nil
	}
	if legacySystems, _ := line["systems"].([]any); len(legacySystems) > 0 {
		for _, rawSystem := range legacySystems {
			if err := appendEntry(rawSystem); err != nil {
				return nil, err
			}
		}
	}
	if rawSystem, ok := line["system"]; ok {
		if err := appendEntry(rawSystem); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func systemInitRecordID(agentKey string, cacheKey string, fingerprint string) string {
	return strings.TrimSpace(agentKey) + "\x00" + strings.TrimSpace(cacheKey) + "\x00" + strings.TrimSpace(fingerprint)
}

func lineIsSystemInitQuery(line map[string]any) bool {
	if strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
		return false
	}
	query, _ := line["query"].(map[string]any)
	return strings.TrimSpace(stringFromAny(query["kind"])) == "system-init"
}

func parseCacheKey(cacheKey string) (string, string) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return "", ""
	}
	mode, stage, ok := strings.Cut(cacheKey, ":")
	if !ok {
		return strings.TrimSpace(cacheKey), ""
	}
	return strings.TrimSpace(mode), strings.TrimSpace(stage)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
