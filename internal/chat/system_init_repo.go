package chat

import (
	"encoding/json"
	"os"
	"strings"
)

func (s *FileStore) LoadSystemInit(chatID string, cacheKey string) (*SystemInitLine, error) {
	inits, latest, err := s.loadSystemInits(chatID)
	if err != nil {
		return nil, err
	}
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return latest, nil
	}
	return inits[cacheKey], nil
}

func (s *FileStore) LoadAllSystemInits(chatID string) (map[string]*SystemInitLine, error) {
	inits, _, err := s.loadSystemInits(chatID)
	return inits, err
}

func (s *FileStore) loadSystemInits(chatID string) (map[string]*SystemInitLine, *SystemInitLine, error) {
	return loadSystemInitsFromPath(s.chatJSONLPath(chatID))
}

func loadSystemInitsFromPath(path string) (map[string]*SystemInitLine, *SystemInitLine, error) {
	lines, err := readJSONLines(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*SystemInitLine{}, nil, nil
		}
		return nil, nil, err
	}
	byCacheKey := map[string]*SystemInitLine{}
	var latest *SystemInitLine
	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		switch lineType {
		case "query":
			rawSystems, _ := line["systems"].([]any)
			if len(rawSystems) == 0 {
				continue
			}
			query, _ := line["query"].(map[string]any)
			for _, rawSystem := range rawSystems {
				systemMap, _ := rawSystem.(map[string]any)
				raw, err := json.Marshal(rawSystem)
				if err != nil {
					return nil, nil, err
				}
				var parsed QueryLineSystemInit
				if err := json.Unmarshal(raw, &parsed); err != nil {
					continue
				}
				cacheKey := strings.TrimSpace(parsed.CacheKey)
				if cacheKey == "" {
					continue
				}
				mode, stage := parseCacheKey(cacheKey)
				converted := SystemInitLine{
					Type:           "system",
					ChatID:         stringFromAny(line["chatId"]),
					AgentKey:       firstNonEmptyString(stringFromAny(line["subAgentKey"]), stringFromAny(line["agentKey"]), stringFromAny(query["agentKey"]), stringFromAny(systemMap["agentKey"])),
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
				byCacheKey[cacheKey] = &convertedCopy
			}
		}
	}
	return byCacheKey, latest, nil
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
