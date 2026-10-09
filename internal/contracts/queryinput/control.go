package queryinput

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	QueryRoleUser       = "user"
	QueryRoleAssistant  = "assistant"
	QueryRoleAutomation = "automation"
	QueryRoleSystem     = "system"

	QueryRoleValidationMessage = "role must be user, assistant, automation, or system"

	ChatSourceQuery            = "query"
	ChatSourceQueryPrefix      = "query:"
	ChatSourceAutomationPrefix = "automation:"
	ChatSourceRunQueryPrefix   = "run-query:"
)

func NormalizeQueryRole(role string) (string, bool) {
	switch strings.TrimSpace(role) {
	case "", QueryRoleUser:
		return QueryRoleUser, true
	case QueryRoleAssistant:
		return QueryRoleAssistant, true
	case QueryRoleAutomation:
		return QueryRoleAutomation, true
	case QueryRoleSystem:
		return QueryRoleSystem, true
	default:
		return "", false
	}
}

func QueryRoleVisible(role string) bool {
	normalized, ok := NormalizeQueryRole(role)
	if !ok {
		return true
	}
	return normalized != QueryRoleAutomation && normalized != QueryRoleSystem
}

type SubmitRequest struct {
	ChatID     string `json:"chatId,omitempty"`
	RunID      string `json:"runId"`
	AgentKey   string `json:"agentKey,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
	AwaitingID string `json:"awaitingId"`
	SubmitID   string `json:"submitId,omitempty"`
	Locale     string `json:"locale,omitempty"`
	// Param carries the single answer of planning/form awaitings; Params
	// carries the item list of question/approval awaitings. They are exclusive.
	Param             SubmitParam  `json:"param,omitempty"`
	Params            SubmitParams `json:"params,omitempty"`
	ContinuationRunID string       `json:"-"`
	ContinuationState any          `json:"-"`
}

// Input returns the submitted answer in the shape its awaiting mode expects.
func (r SubmitRequest) Input() any {
	if r.Param != nil {
		return map[string]any(r.Param)
	}
	return r.Params
}

// WriteInput records the submitted answer under its wire field name.
func (r SubmitRequest) WriteInput(payload map[string]any) {
	WriteSubmitInput(payload, r.Input())
}

// WriteSubmitInput stores a single answer as param and an item list as params.
func WriteSubmitInput(payload map[string]any, input any) {
	switch typed := input.(type) {
	case SubmitParam:
		if typed != nil {
			payload["param"] = map[string]any(typed)
			return
		}
	case map[string]any:
		if typed != nil {
			payload["param"] = typed
			return
		}
	}
	payload["params"] = input
}

type SubmitParam map[string]any

func (p *SubmitParam) UnmarshalJSON(data []byte) error {
	var item map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &item); err != nil || len(item) == 0 {
		return fmt.Errorf("param must be a non-empty object")
	}
	*p = SubmitParam(item)
	return nil
}

// DecodeSubmitSingle reads the answer of a planning/form awaiting.
func DecodeSubmitSingle(input any) (map[string]any, error) {
	switch typed := input.(type) {
	case SubmitParam:
		if len(typed) > 0 {
			return map[string]any(typed), nil
		}
	case map[string]any:
		if len(typed) > 0 {
			return typed, nil
		}
	}
	return nil, fmt.Errorf("param must be a non-empty object")
}

// DecodeSubmitItems reads the item list of a question/approval awaiting.
func DecodeSubmitItems(input any) ([]map[string]any, error) {
	switch typed := input.(type) {
	case nil:
		return nil, nil
	case SubmitParams:
		return DecodeSubmitParams(typed)
	case []json.RawMessage:
		return DecodeSubmitParams(SubmitParams(typed))
	case []map[string]any:
		return typed, nil
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok || len(item) == 0 {
				return nil, fmt.Errorf("submit items must be objects")
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("submit params must be an array")
	}
}

type SubmitParams []json.RawMessage

func (p *SubmitParams) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("params must be an array")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("params must be an array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("params must be an array")
	}
	*p = SubmitParams(raw)
	return nil
}

func (p SubmitParams) MarshalJSON() ([]byte, error) {
	return json.Marshal([]json.RawMessage(p))
}

func (p SubmitParams) Empty() bool {
	return len(p) == 0
}

func DecodeSubmitParam(raw json.RawMessage) (map[string]any, error) {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("submit items must be objects")
	}
	if len(item) == 0 {
		return nil, fmt.Errorf("submit items must be objects")
	}
	return item, nil
}

func DecodeSubmitParams(params SubmitParams) ([]map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(params))
	for _, raw := range params {
		item, err := DecodeSubmitParam(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func EncodeSubmitParams(value any) (SubmitParams, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var params SubmitParams
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, err
	}
	return params, nil
}

type SubmitResponse struct {
	Accepted   bool   `json:"accepted"`
	Status     string `json:"status"`
	ChatID     string `json:"chatId,omitempty"`
	RunID      string `json:"runId"`
	AwaitingID string `json:"awaitingId"`
	SubmitID   string `json:"submitId,omitempty"`
	Continued  bool   `json:"continued,omitempty"`
	ErrorCode  string `json:"errorCode,omitempty"`
	Detail     string `json:"detail"`
}

type SteerRequest struct {
	RequestID  string      `json:"requestId,omitempty"`
	ChatID     string      `json:"chatId,omitempty"`
	RunID      string      `json:"runId"`
	SteerID    string      `json:"steerId,omitempty"`
	AgentKey   string      `json:"agentKey,omitempty"`
	TeamID     string      `json:"teamId,omitempty"`
	Message    string      `json:"message"`
	References []Reference `json:"references,omitempty"`
	// PreparedMessages is an immutable, server-prepared input; never accepted from the wire.
	PreparedMessages []map[string]any `json:"-"`
}

type SteerResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	SteerID  string `json:"steerId"`
	Detail   string `json:"detail"`
}

type InterruptRequest struct {
	RequestID       string `json:"requestId,omitempty"`
	ChatID          string `json:"chatId,omitempty"`
	RunID           string `json:"runId"`
	AgentKey        string `json:"agentKey,omitempty"`
	TeamID          string `json:"teamId,omitempty"`
	Message         string `json:"message,omitempty"`
	InterruptSource string `json:"source,omitempty"`
	InterruptReason string `json:"reason,omitempty"`
	InterruptDetail string `json:"detail,omitempty"`
}

type InterruptResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	Detail   string `json:"detail"`
}

type AccessLevelRequest struct {
	RequestID   string `json:"requestId,omitempty"`
	RunID       string `json:"runId"`
	AgentKey    string `json:"agentKey,omitempty"`
	TeamID      string `json:"teamId,omitempty"`
	AccessLevel string `json:"accessLevel"`
	Reason      string `json:"reason,omitempty"`
}

type AccessLevelResponse struct {
	Accepted            bool   `json:"accepted"`
	Status              string `json:"status"`
	RunID               string `json:"runId"`
	PreviousAccessLevel string `json:"previousAccessLevel,omitempty"`
	AccessLevel         string `json:"accessLevel"`
	Version             int64  `json:"version"`
	Detail              string `json:"detail"`
}

type CompactResponse struct {
	CycleID                    string         `json:"cycleId,omitempty"`
	CycleComplete              *bool          `json:"cycleComplete,omitempty"`
	Accepted                   bool           `json:"accepted"`
	Status                     string         `json:"status"`
	RequestID                  string         `json:"requestId,omitempty"`
	ChatID                     string         `json:"chatId,omitempty"`
	RunID                      string         `json:"runId,omitempty"`
	CompactID                  string         `json:"compactId,omitempty"`
	Trigger                    string         `json:"trigger,omitempty"`
	Scope                      string         `json:"scope,omitempty"`
	Level                      string         `json:"level,omitempty"`
	SummarySource              string         `json:"summarySource,omitempty"`
	PreCompactEstimatedTokens  int            `json:"preCompactEstimatedTokens,omitempty"`
	PostCompactEstimatedTokens int            `json:"postCompactEstimatedTokens,omitempty"`
	CompressionRatio           float64        `json:"compressionRatio,omitempty"`
	RemainingRatio             float64        `json:"remainingRatio,omitempty"`
	ReleasedRatio              float64        `json:"releasedRatio,omitempty"`
	CompactionUsage            map[string]any `json:"compactionUsage,omitempty"`
	ToolsCleared               int            `json:"toolsCleared,omitempty"`
	ReasoningCleared           int            `json:"reasoningCleared,omitempty"`
	ToolsKept                  int            `json:"toolsKept,omitempty"`
	TokensFreed                int            `json:"tokensFreed,omitempty"`
	Detail                     string         `json:"detail,omitempty"`
	Retryable                  bool           `json:"retryable,omitempty"`
}
