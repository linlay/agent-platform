package types

import (
	"encoding/json"

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
	// OriginalQuery is the immutable incoming JSON, separate from resolved inputs.
	OriginalQuery json.RawMessage `json:"-"`

	// InitialChatName is internal-only and applies to newly created Chats.
	InitialChatName string `json:"-"`
	// SkipInteractionPolicy is set only by Runtime for a chat_start admission.
	// interactionConfig gates what a user may select in a conversation; it does
	// not gate a trusted Agent tool entry. No transport decodes this field.
	SkipInteractionPolicy bool `json:"-"`
	// Lane is a side lane (btw or explain) for a hidden branch Run; empty is
	// the main lane. SideID continues an existing branch of that lane.
	Lane           string
	SideID         string
	TrustedGateway bool
	RequestID      string
	RunID          string
	ChatID         string
	AgentKey       string

	// Role is the model input role. Admission derives it from Origin; no
	// request carries it.
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
	AccessLevel                string
	Model                      *QueryModelOptions
	SyntheticQueryBootstrapped bool
	// Origin is who sent the input; admission defaults it to the requesting user.
	Origin *queryinput.Origin
	// AccessGrant audits how a chat_start Run obtained its access level.
	AccessGrant map[string]any `json:"-"`
	// PlanRunID links a Run started by approving a plan to its planning Run.
	PlanRunID       string `json:"-"`
	Identity        *contracts.AuthIdentity
	Caller          Caller
	Locale          string
	ClientTarget    ClientTarget
	ChatSource      string
	ResourceBaseURL string
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
	SideID       string
	ChatID       string
	RunID        string
	ErrorPayload map[string]any
	Completion   *chat.RunCompletion
	Content      string
	FullText     string
	ErrorMessage string
}
