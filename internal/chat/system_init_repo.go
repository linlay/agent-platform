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
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
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
				converted := SystemInitLine{
					Type:          "system",
					ChatID:        stringFromAny(line["chatId"]),
					AgentKey:      firstNonEmptyString(parsed.AgentKey, stringFromAny(line["agentKey"]), stringFromAny(query["agentKey"])),
					RunID:         stringFromAny(line["runId"]),
					CreatedAt:     int64FromAny(line["updatedAt"]),
					Fingerprint:   parsed.Fingerprint,
					CacheKey:      parsed.CacheKey,
					Mode:          parsed.Mode,
					Stage:         parsed.Stage,
					SystemMessage: parsed.SystemMessage,
					Tools:         parsed.Tools,
				}
				convertedCopy := converted
				latest = &convertedCopy
				byCacheKey[cacheKey] = &convertedCopy
			}
		case "system", "system-init":
			raw, err := json.Marshal(line)
			if err != nil {
				return nil, nil, err
			}
			var parsed SystemInitLine
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, nil, err
			}
			parsedCopy := parsed
			latest = &parsedCopy
			if cacheKey := strings.TrimSpace(parsed.CacheKey); cacheKey != "" {
				byCacheKey[cacheKey] = &parsedCopy
			}
		}
	}
	return byCacheKey, latest, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
