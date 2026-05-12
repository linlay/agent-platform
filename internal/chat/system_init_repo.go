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
		if lineType != "system" && lineType != "system-init" {
			continue
		}
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
	return byCacheKey, latest, nil
}
