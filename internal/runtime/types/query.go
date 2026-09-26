package types

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
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
	SideQuery                  bool
	SideQueryID                string
	TrustedGateway             bool
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
	Identity                   *contracts.AuthIdentity
	Caller                     Caller
	Locale                     string
	ClientTarget               ClientTarget
	ChatSource                 string
	ResourceBaseURL            string
}

type QueryModelOptions = queryinput.QueryModelOptions

type Scene = queryinput.Scene

type Reference = queryinput.Reference

type QueryHooks struct {
	OnRunStarted func(chat.RunStart)
}

type QueryResult struct {
	Usage        chat.UsageData
	FinishReason string
	SideQueryID  string
	ChatID       string
	RunID        string
	ErrorPayload map[string]any
	Completion   *chat.RunCompletion
	Content      string
	FullText     string
	ErrorMessage string
}
