package types

import (
	"agent-platform/internal/chat"
)

// Caller is the authenticated runtime identity supplied by a transport or an
// in-process caller. Runtime code must not depend on HTTP or WebSocket types.
type Caller struct {
	Subject  string
	DeviceID string
	Scope    string
}

type ClientTarget struct {
	SessionID   string
	BoundaryKey string
	Subject     string
	SurfaceID   string
}

// QueryCommand is the transport-neutral envelope accepted by the runtime.
// HTTP and WebSocket adapters are responsible for decoding external DTOs into
// this value before invoking application behavior.
type QueryCommand struct {
	RequestID                  string
	RunID                      string
	ChatID                     string
	AgentKey                   string
	TeamID                     string
	Role                       string
	Hidden                     *bool
	Message                    string
	SourceUser                 string
	References                 []Reference
	Params                     map[string]any
	Scene                      *Scene
	Stream                     *bool
	IncludeUsage               bool
	IncludeFullText            bool
	PlanningMode               *bool
	EditingMode                *bool
	MustUseSkills              []string
	AccessLevel                string
	Model                      *QueryModelOptions
	SyntheticQueryBootstrapped bool
	TrustedQueryMetadata       map[string]any
	Caller                     Caller
	Locale                     string
	ClientTarget               ClientTarget
	ChatSource                 string
	ResourceBaseURL            string
}

type QueryModelOptions struct {
	Key             string `json:"key,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
}

type Scene struct {
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type Reference struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type,omitempty"`
	Name      string         `json:"name,omitempty"`
	Path      string         `json:"path,omitempty"`
	MimeType  string         `json:"mimeType,omitempty"`
	SizeBytes *int64         `json:"sizeBytes,omitempty"`
	URL       string         `json:"url,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type QueryHooks struct {
	OnRunStarted func(chat.RunStart)
}

type QueryResult struct {
	Completion   *chat.RunCompletion
	Content      string
	FullText     string
	ErrorMessage string
}
