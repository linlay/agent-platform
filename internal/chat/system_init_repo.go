package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type SystemInitKey struct {
	AgentKey string
	CacheKey string
}

// systemInitRef identifies the immutable system snapshot used by one LLM
// call. Unlike SystemInitKey, it includes the fingerprint so callers can
// retrieve a historical snapshot after a later catalog change writes a new
// snapshot under the same agent/cache key.
type systemInitRef struct {
	AgentKey    string
	CacheKey    string
	Fingerprint string
}

type SystemInitIndex map[SystemInitKey]*SystemInitLine

func (index SystemInitIndex) Lookup(agentKey string, cacheKey string) *SystemInitLine {
	key := SystemInitKey{AgentKey: strings.TrimSpace(agentKey), CacheKey: strings.TrimSpace(cacheKey)}
	if key.AgentKey == "" || key.CacheKey == "" {
		return nil
	}
	return index[key]
}

func (s *FileStore) LoadSystemInit(chatID string, key SystemInitKey) (*SystemInitLine, error) {
	inits, err := s.loadSystemInits(chatID)
	if err != nil {
		return nil, err
	}
	return inits.Lookup(key.AgentKey, key.CacheKey), nil
}

// LoadRunSystemInit returns the initial system snapshot used by an agent in a
// run. It first prefers a system-init record written in that run, then follows
// the first persisted step systemRef back to the immutable snapshot. The
// latter is necessary when a run reuses a snapshot originally written by an
// earlier run.
func (s *FileStore) LoadRunSystemInit(chatID string, runID string, agentKey string) (*SystemInitLine, error) {
	chatID = strings.TrimSpace(chatID)
	runID = strings.TrimSpace(runID)
	agentKey = strings.TrimSpace(agentKey)
	if chatID == "" || runID == "" || agentKey == "" {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	summary, err := s.loadSummary(chatID)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrChatNotFound
	}

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if strings.TrimSpace(stringFromAny(line["runId"])) != runID || strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
			continue
		}
		system, err := queryLineSystemFromJSONL(line)
		if err != nil {
			return nil, err
		}
		if system == nil || system.AgentKey != agentKey {
			continue
		}
		result := systemInitLineFromQueryLine(line, system)
		return &result, nil
	}

	for _, line := range lines {
		if strings.TrimSpace(stringFromAny(line["runId"])) != runID || !lineIsStep(line) {
			continue
		}
		refMap, err := stepSystemRefFromJSONL(line, false)
		if err != nil {
			return nil, err
		}
		ref := systemInitRef{
			AgentKey:    strings.TrimSpace(stringFromAny(refMap["agentKey"])),
			CacheKey:    strings.TrimSpace(stringFromAny(refMap["cacheKey"])),
			Fingerprint: strings.TrimSpace(stringFromAny(refMap["fingerprint"])),
		}
		if ref.AgentKey != agentKey || ref.CacheKey == "" || ref.Fingerprint == "" {
			continue
		}
		return findSystemInitByRef(lines, ref)
	}
	return nil, nil
}

func findSystemInitByRef(lines []map[string]any, ref systemInitRef) (*SystemInitLine, error) {
	for _, line := range lines {
		if strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
			continue
		}
		system, err := queryLineSystemFromJSONL(line)
		if err != nil {
			return nil, err
		}
		if system == nil || system.AgentKey != ref.AgentKey || system.CacheKey != ref.CacheKey || system.Fingerprint != ref.Fingerprint {
			continue
		}
		result := systemInitLineFromQueryLine(line, system)
		return &result, nil
	}
	return nil, nil
}

func (s *FileStore) LoadAllSystemInits(chatID string) (SystemInitIndex, error) {
	return s.loadSystemInits(chatID)
}

func (s *FileStore) loadSystemInits(chatID string) (SystemInitIndex, error) {
	return loadSystemInitsFromPath(s.chatJSONLPath(chatID))
}

func loadSystemInitsFromPath(path string) (SystemInitIndex, error) {
	lines, err := readPersistedJSONLines(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SystemInitIndex{}, nil
		}
		return nil, err
	}
	byKey := SystemInitIndex{}
	for _, line := range lines {
		if strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
			continue
		}
		system, err := queryLineSystemFromJSONL(line)
		if err != nil {
			return nil, err
		}
		if system == nil {
			continue
		}
		converted := systemInitLineFromQueryLine(line, system)
		convertedCopy := converted
		byKey[SystemInitKey{AgentKey: system.AgentKey, CacheKey: system.CacheKey}] = &convertedCopy
	}
	return byKey, nil
}

func systemInitLineFromQueryLine(line map[string]any, system *QueryLineSystem) SystemInitLine {
	mode, stage := parseCacheKey(system.CacheKey)
	return SystemInitLine{
		Type:           "system",
		ChatID:         stringFromAny(line["chatId"]),
		AgentKey:       system.AgentKey,
		RunID:          stringFromAny(line["runId"]),
		CreatedAt:      int64FromAny(line["updatedAt"]),
		Fingerprint:    system.Fingerprint,
		CacheKey:       system.CacheKey,
		Mode:           mode,
		Stage:          stage,
		SystemMessage:  system.SystemMessage,
		Tools:          system.Tools,
		Model:          system.Model,
		ToolChoice:     system.ToolChoice,
		RequestOptions: system.RequestOptions,
	}
}

// queryLineSystemFromJSONL decodes the sole supported query-level system
// snapshot. The snapshot is optional, but when present its identity is always
// complete and self-contained; no query or task metadata is used as a fallback.
func queryLineSystemFromJSONL(line map[string]any) (*QueryLineSystem, error) {
	if len(line) == 0 {
		return nil, nil
	}
	if _, found := line["systems"]; found {
		return nil, systemSchemaError(line, "unsupported system schema field=systems")
	}
	rawSystem, found := line["system"]
	if !found {
		return nil, nil
	}
	systemMap, ok := rawSystem.(map[string]any)
	if !ok {
		return nil, systemSchemaError(line, "invalid system field=system must be an object")
	}
	raw, err := json.Marshal(systemMap)
	if err != nil {
		return nil, systemSchemaError(line, "invalid system field=system")
	}
	var parsed QueryLineSystem
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, systemSchemaError(line, "invalid system field=system")
	}
	parsed.AgentKey = strings.TrimSpace(parsed.AgentKey)
	parsed.CacheKey = strings.TrimSpace(parsed.CacheKey)
	parsed.Fingerprint = strings.TrimSpace(parsed.Fingerprint)
	if parsed.AgentKey == "" {
		return nil, systemSchemaError(line, "invalid system missing=agentKey")
	}
	if parsed.CacheKey == "" {
		return nil, systemSchemaError(line, "invalid system missing=cacheKey")
	}
	if parsed.Fingerprint == "" {
		return nil, systemSchemaError(line, "invalid system missing=fingerprint")
	}
	return &parsed, nil
}

func validatePersistedSystemInitSchema(lines []map[string]any) error {
	for _, line := range lines {
		lineType := strings.TrimSpace(stringFromAny(line["_type"]))
		if lineType != "query" && !lineIsStep(line) {
			continue
		}
		if _, found := line["systems"]; found {
			return systemSchemaError(line, "unsupported system schema field=systems")
		}
		if lineType == "query" {
			if _, err := queryLineSystemFromJSONL(line); err != nil {
				return err
			}
			continue
		}
		if _, found := line["system"]; found {
			return systemSchemaError(line, "unsupported system schema field=system")
		}
		if _, err := stepSystemRefFromJSONL(line, false); err != nil {
			return err
		}
	}
	return nil
}

func stepSystemRefFromJSONL(line map[string]any, required bool) (map[string]any, error) {
	rawRef, found := line["systemRef"]
	if !found {
		if required {
			return nil, systemSchemaError(line, "invalid systemRef missing=systemRef")
		}
		return nil, nil
	}
	ref, ok := rawRef.(map[string]any)
	if !ok {
		return nil, systemSchemaError(line, "invalid systemRef field=systemRef must be an object")
	}
	agentKey := strings.TrimSpace(stringFromAny(ref["agentKey"]))
	cacheKey := strings.TrimSpace(stringFromAny(ref["cacheKey"]))
	fingerprint := strings.TrimSpace(stringFromAny(ref["fingerprint"]))
	if agentKey == "" {
		return nil, systemSchemaError(line, "invalid systemRef missing=agentKey")
	}
	if cacheKey == "" {
		return nil, systemSchemaError(line, "invalid systemRef missing=cacheKey")
	}
	if fingerprint == "" {
		return nil, systemSchemaError(line, "invalid systemRef missing=fingerprint")
	}
	return map[string]any{
		"agentKey":    agentKey,
		"cacheKey":    cacheKey,
		"fingerprint": fingerprint,
	}, nil
}

func systemSchemaError(line map[string]any, reason string) error {
	return fmt.Errorf("system-init schema error: chatId=%s runId=%s %s", strings.TrimSpace(stringFromAny(line["chatId"])), strings.TrimSpace(stringFromAny(line["runId"])), reason)
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
