package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/skills"
)

const (
	KindFact        = "fact"
	KindObservation = "observation"

	ScopeUser   = "user"
	ScopeAgent  = "agent"
	ScopeTeam   = "team"
	ScopeChat   = "chat"
	ScopeGlobal = "global"

	StatusActive      = "active"
	StatusOpen        = "open"
	StatusSuperseded  = "superseded"
	StatusArchived    = "archived"
	StatusContested   = "contested"
	defaultUserKey    = "_local_default"
	defaultGlobalKey  = "global:default"
	defaultTeamKey    = "team:default"
	defaultChatStatus = StatusOpen
)

type ContextRequest struct {
	AgentKey        string
	TeamID          string
	ChatID          string
	UserKey         string
	Query           string
	TopFacts        int
	TopObs          int
	MaxChars        int
	AvailableTokens int
}

type Layer string

const (
	LayerStable      Layer = "stable"
	LayerObservation Layer = "observation"
	LayerSession     Layer = "session"
	LayerRawTrace    Layer = "raw_trace"
)

type SelectionReason string

const (
	SelectionReasonScopeMatch  SelectionReason = "scope_match"
	SelectionReasonQueryMatch  SelectionReason = "query_match"
	SelectionReasonHighRank    SelectionReason = "high_rank"
	SelectionReasonSnapshotPin SelectionReason = "snapshot_pinned"
	SelectionReasonHybridScore SelectionReason = "hybrid_score"
)

type DisclosureDecision struct {
	Layer   Layer
	ItemIDs []string
	Reason  string
}

type MemorySnapshot struct {
	ID              string
	ChatID          string
	AgentKey        string
	StableItemIDs   []string
	ObservedItemIDs []string
}

type ContextBundle struct {
	StableFacts          []api.StoredMemoryResponse
	SessionSummaries     []api.StoredMemoryResponse
	RelevantObservations []api.StoredMemoryResponse
	DisclosedLayers      []string
	StopReason           string
	SnapshotID           string
	CandidateCounts      map[string]int
	SelectedCounts       map[string]int
	Decisions            []DisclosureDecision
	StablePrompt         string
	SessionPrompt        string
	ObservationPrompt    string
}

type LearnInput struct {
	Request         api.LearnRequest
	Trace           chat.RunTrace
	AgentKey        string
	TeamID          string
	UserKey         string
	SkillCandidates skills.CandidateStore
}

func normalizeStoredItem(item api.StoredMemoryResponse) api.StoredMemoryResponse {
	item.Kind = normalizeMemoryKind(item.Kind)
	item.SourceType = normalizeSourceType(item.SourceType)
	item.Category = normalizeCategory(item.Category)
	item.Importance = normalizeImportance(item.Importance)
	item.ScopeType = normalizeScopeType(item.ScopeType)
	item.ScopeKey = normalizeScopeKey(item.ScopeType, item.ScopeKey, item.AgentKey, "", item.ChatID, "")
	item.Title = normalizeMemoryTitle(item.Title, item.Summary)
	item.Status = normalizeMemoryStatus(item.Status, item.Kind)
	item.Confidence = normalizeMemoryConfidence(item.Confidence, item.Kind)
	item.Tags = normalizeTags(item.Tags)
	item.SubjectKey = normalizeSubjectKey(item.SubjectKey, item.ChatID, item.AgentKey)
	if strings.TrimSpace(item.RefID) == "" {
		item.RefID = item.ID
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = item.UpdatedAt
	}
	if item.UpdatedAt == 0 {
		item.UpdatedAt = item.CreatedAt
	}
	return item
}

func buildSnapshotID(agentKey string, chatID string, stableItems []api.StoredMemoryResponse, observationItems []api.StoredMemoryResponse) string {
	h := sha1.New()
	writeSnapshotPart := func(value string) {
		_, _ = h.Write([]byte(strings.TrimSpace(value)))
		_, _ = h.Write([]byte{0})
	}
	writeSnapshotPart(agentKey)
	writeSnapshotPart(chatID)
	for _, item := range stableItems {
		writeSnapshotPart(item.ID)
	}
	for _, item := range observationItems {
		writeSnapshotPart(item.ID)
	}
	return "snap_" + hex.EncodeToString(h.Sum(nil))[:12]
}

func normalizeMemoryKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), KindObservation) {
		return KindObservation
	}
	return KindFact
}

func normalizeScopeType(scopeType string) string {
	switch strings.ToLower(strings.TrimSpace(scopeType)) {
	case ScopeUser:
		return ScopeUser
	case ScopeTeam:
		return ScopeTeam
	case ScopeChat:
		return ScopeChat
	case ScopeGlobal:
		return ScopeGlobal
	default:
		return ScopeAgent
	}
}

func normalizeScopeKey(scopeType string, scopeKey string, agentKey string, teamID string, chatID string, userKey string) string {
	if strings.TrimSpace(scopeKey) != "" {
		return strings.TrimSpace(scopeKey)
	}
	switch normalizeScopeType(scopeType) {
	case ScopeUser:
		if strings.TrimSpace(userKey) == "" {
			userKey = defaultUserKey
		}
		return "user:" + strings.TrimSpace(userKey)
	case ScopeTeam:
		if strings.TrimSpace(teamID) == "" {
			return defaultTeamKey
		}
		return "team:" + strings.TrimSpace(teamID)
	case ScopeChat:
		if strings.TrimSpace(chatID) == "" {
			return "chat:unknown"
		}
		return "chat:" + strings.TrimSpace(chatID)
	case ScopeGlobal:
		return defaultGlobalKey
	default:
		if strings.TrimSpace(agentKey) == "" {
			return "agent:default"
		}
		return "agent:" + strings.TrimSpace(agentKey)
	}
}

func normalizeMemoryTitle(title string, fallback string) string {
	if strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "Untitled Memory"
	}
	if len([]rune(fallback)) <= 72 {
		return fallback
	}
	runes := []rune(fallback)
	return strings.TrimSpace(string(runes[:72])) + "..."
}

func normalizeMemoryStatus(status string, kind string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusActive:
		return StatusActive
	case StatusOpen:
		if normalizeMemoryKind(kind) == KindObservation {
			return StatusOpen
		}
		return StatusActive
	case StatusSuperseded:
		return StatusSuperseded
	case StatusArchived:
		return StatusArchived
	case StatusContested:
		return StatusContested
	default:
		if normalizeMemoryKind(kind) == KindObservation {
			return defaultChatStatus
		}
		return StatusActive
	}
}

func normalizeMemoryConfidence(confidence float64, kind string) float64 {
	if confidence <= 0 {
		if normalizeMemoryKind(kind) == KindObservation {
			return 0.7
		}
		return 0.9
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func buildLearnResponse(input LearnInput, stored []api.StoredMemoryResponse) api.LearnResponse {
	status := "stored"
	if len(stored) == 0 {
		status = "no_memory_extracted"
	}
	return api.LearnResponse{
		Accepted:         len(stored) > 0,
		Status:           status,
		RequestID:        input.Request.RequestID,
		ChatID:           input.Request.ChatID,
		ObservationCount: len(stored),
		Stored:           append([]api.StoredMemoryResponse(nil), stored...),
	}
}

func scopeMatches(item api.StoredMemoryResponse, request ContextRequest) bool {
	switch normalizeScopeType(item.ScopeType) {
	case ScopeUser:
		return strings.TrimSpace(item.ScopeKey) == normalizeScopeKey(ScopeUser, "", "", "", "", request.UserKey)
	case ScopeTeam:
		return strings.TrimSpace(item.ScopeKey) == normalizeScopeKey(ScopeTeam, "", "", request.TeamID, "", "")
	case ScopeChat:
		return strings.TrimSpace(item.ScopeKey) == normalizeScopeKey(ScopeChat, "", "", "", request.ChatID, "")
	case ScopeGlobal:
		return true
	default:
		return strings.TrimSpace(item.ScopeKey) == normalizeScopeKey(ScopeAgent, "", request.AgentKey, "", "", "")
	}
}

func classifyObservationCategory(text string) string {
	needle := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(needle, "bug"):
		return "bugfix"
	case strings.Contains(needle, "fix"):
		return "bugfix"
	case strings.Contains(needle, "todo"):
		return "todo"
	default:
		return "general"
	}
}

func summarizeObservationTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Observed run outcome"
	}
	return normalizeMemoryTitle("", text)
}

func observationScopeKey(input LearnInput) string {
	return normalizeScopeKey(ScopeChat, "", input.AgentKey, "", input.Request.ChatID, "")
}

func factScopeKey(scopeType string, input LearnInput) string {
	return normalizeScopeKey(scopeType, "", input.AgentKey, input.TeamID, input.Request.ChatID, input.UserKey)
}

func formatScopeLabel(scopeType string) string {
	switch normalizeScopeType(scopeType) {
	case ScopeUser:
		return "USER"
	case ScopeTeam:
		return "TEAM"
	case ScopeChat:
		return "CHAT"
	case ScopeGlobal:
		return "GLOBAL"
	default:
		return "AGENT"
	}
}

func memoryLine(item api.StoredMemoryResponse) string {
	parts := []string{
		fmt.Sprintf("[%s]", normalizeCategory(item.Category)),
		fmt.Sprintf("[score=%.2f]", normalizeMemoryConfidence(item.Confidence, item.Kind)),
		fmt.Sprintf("[%s]", item.ID),
	}
	if strings.TrimSpace(item.Title) != "" {
		parts = append(parts, item.Title)
	}
	if strings.TrimSpace(item.Summary) != "" {
		parts = append(parts, item.Summary)
	}
	return strings.Join(parts, " ")
}
