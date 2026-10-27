// Package mailbox owns durable, Chat-addressed Agent correspondence. Runtime
// supplies admission and execution through Port; this package has no transport.
package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/controlscope"
)

type Kind string

const (
	Letter Kind = "letter"
	Reply  Kind = "reply"
	Notify Kind = "notify"
	Ask    Kind = "ask"
)

type Status string

const (
	Accepting Status = "accepting"
	Queued    Status = "queued"
	Inflight  Status = "inflight"
	Delivered Status = "delivered"
	Done      Status = "done"
	Failed    Status = "failed"
	Suspended Status = "suspended"
	Discarded Status = "discarded"
	Withdrawn Status = "withdrawn"
)

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path"`
	Source string `json:"source,omitempty"`
}
type Mail struct {
	ID             string                        `json:"mailId"`
	Kind           Kind                          `json:"kind"`
	Status         Status                        `json:"status"`
	CreatedAt      int64                         `json:"createdAt"`
	UpdatedAt      int64                         `json:"updatedAt"`
	ToAgentKey     string                        `json:"toAgentKey"`
	ToChatID       string                        `json:"toChatId"`
	OpenedChat     bool                          `json:"openedChat,omitempty"`
	OriginType     string                        `json:"originType"`
	FromAgentKey   string                        `json:"fromAgentKey"`
	FromChatID     string                        `json:"fromChatId"`
	FromRunID      string                        `json:"fromRunId"`
	FromToolID     string                        `json:"fromToolId,omitempty"`
	Subject        string                        `json:"-"`
	Input          json.RawMessage               `json:"input,omitempty"`
	Message        string                        `json:"message"`
	References     []queryinput.Reference        `json:"references,omitempty"`
	Files          []File                        `json:"files,omitempty"`
	PurgedAt       int64                         `json:"purgedAt,omitempty"`
	Hop            int                           `json:"hop"`
	AccessLevel    string                        `json:"accessLevel,omitempty"`
	AccessGrant    map[string]any                `json:"accessGrant,omitempty"`
	Model          *queryinput.QueryModelOptions `json:"model,omitempty"`
	RequestDigest  string                        `json:"-"`
	AnsweredBy     string                        `json:"answeredBy,omitempty"`
	ReplyKey       string                        `json:"replyKey,omitempty"`
	ReplyOfRun     string                        `json:"replyOfRun,omitempty"`
	Outcome        string                        `json:"outcome,omitempty"`
	AwaitingID     string                        `json:"awaitingId,omitempty"`
	AwaitingMode   string                        `json:"awaitingMode,omitempty"`
	AwaitingSource string                        `json:"awaitingSource,omitempty"`
	// ParentID retains the originating letter for asks and notifications even
	// after a delivery attempt has moved to another Run.
	ParentID    string         `json:"parentId,omitempty"`
	Delivery    string         `json:"delivery,omitempty"`
	RunID       string         `json:"runId,omitempty"`
	SteerID     string         `json:"steerId,omitempty"`
	Attempts    int            `json:"attempts"`
	ConsumedAt  int64          `json:"consumedAt,omitempty"`
	ResumePhase Status         `json:"resumePhase,omitempty"`
	Evidence    string         `json:"evidence,omitempty"`
	Error       map[string]any `json:"error,omitempty"`
}

func (m Mail) Terminal() bool {
	return m.Status == Done || m.Status == Failed || m.Status == Discarded || m.Status == Withdrawn || (m.Status == Delivered && (m.Kind == Reply || m.Kind == Notify))
}
func (m Mail) Metadata() *queryinput.MailInput {
	return &queryinput.MailInput{ID: m.ID, Kind: string(m.Kind), Attempt: m.Attempts, Hop: m.Hop, Outcome: m.Outcome}
}

type Request struct {
	Start              contracts.RunStartRequest
	NewChat            bool
	Files              []string
	Hop                int
	UserChatReferences []string
}
type Prepared struct {
	Request    contracts.RunStartRequest
	Plan       contracts.RunStartPlan
	ChatID     string
	OpenedChat bool
	Primary    bool
}
type Grant struct {
	AccessLevel string
	AccessGrant map[string]any
	Scope       controlscope.Scope
}
type Target struct {
	RunID string
	Queue bool
}
type DeliveryResult struct {
	Accepted bool
	Rejected bool
	Unknown  bool
	RunID    string
}

// Port is implemented by Runtime. Accept must commit persist while holding the
// caller's live permission lock, after consuming any required one-shot review.
type Port interface {
	PrepareMail(context.Context, Request) (Prepared, error)
	AcceptMail(context.Context, Request, Prepared, func(Grant) error) error
	EnsureMailChat(context.Context, *Mail) error
	MailTarget(context.Context, Mail) (Target, error)
	DeliverMail(context.Context, Mail, controlscope.Scope) (DeliveryResult, error)
	SubmitMail(context.Context, Mail, queryinput.SubmitRequest, *contracts.ExecutionContext) (queryinput.SubmitResponse, error)
}
type Filter struct {
	NewestFirst bool
	AllSubjects bool
	AgentKey    string
	ChatID      string
	Kind        Kind
	Status      Status
	Subject     string
	Limit       int
	Offset      int
}
type Receipt struct {
	MailID   string         `json:"mailId"`
	ChatID   string         `json:"chatId"`
	Delivery string         `json:"delivery"`
	RunID    string         `json:"runId,omitempty"`
	Status   Status         `json:"status"`
	Error    map[string]any `json:"error,omitempty"`
}

func receipt(m Mail) Receipt {
	d := m.Delivery
	if d == "" {
		d = "queued"
	}
	return Receipt{MailID: m.ID, ChatID: m.ToChatID, Delivery: d, RunID: m.RunID, Status: m.Status, Error: m.Error}
}
func failure(code, message string) error {
	return &contracts.RunToolError{Code: code, Message: message}
}
func errorData(err error) map[string]any {
	if e, ok := err.(*contracts.RunToolError); ok {
		return map[string]any{"code": e.Code, "message": e.Message}
	}
	return map[string]any{"code": "mail_delivery_failed", "message": fmt.Sprint(err)}
}
func now() int64 { return time.Now().UnixMilli() }
