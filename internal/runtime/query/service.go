package query

import (
	"context"
	"errors"
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/reference"
	"agent-platform/internal/runtime/runstate"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/toolinteraction"
)

var ErrNotConfigured = errors.New("query runtime is not configured")

type statusError = runtimetypes.RequestError
type DeferredAwaitingStore interface {
	Register(runtimetypes.DeferredAwaiting)
	Lookup(string) (runtimetypes.DeferredAwaiting, bool)
	Remove(string)
	LockResolution(string, string, string) func()
}

// ProxyPort owns only non-native protocol routing and forwarding. Native runs
// never delegate admission, recovery or execution back to the transport.
type ProxyPort interface {
	Configure(*catalog.AgentDefinition) *runtimetypes.RequestError
	Models(string) ([]queryinput.CoderModelOption, error, bool)
	Start(runtimetypes.PreparedQuery, runtimetypes.RegisteredRun, *stream.RunEventBus, bool) error
	Execute(runtimetypes.PreparedQuery, runtimetypes.RegisteredRun, *stream.RunEventBus) (runtimetypes.QueryResult, error)
	Submit(queryinput.SubmitRequest) (queryinput.SubmitResponse, *runtimetypes.RequestError, bool)
	Steer(queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool)
	Interrupt(queryinput.InterruptRequest) (queryinput.InterruptResponse, *runtimetypes.RequestError, bool)
	AccessLevel(queryinput.AccessLevelRequest) (queryinput.AccessLevelResponse, *runtimetypes.RequestError, bool)
}
type Dependencies struct {
	BackgroundContext context.Context
	Config            config.Config
	Runs              contracts.RunManager
	Chats             chat.Store
	Registry          catalog.Registry
	Models            *models.ModelRegistry
	Tools             contracts.ToolExecutor
	Agent             runtimetypes.Engine
	Profiles          sessionbuild.ProfileBuilder
	Sessions          *sessionbuild.Builder
	References        *reference.Service
	Notifications     contracts.NotificationSink
	ToolInteractions  *toolinteraction.Registry
	DeltaMappers      contracts.StreamDeltaMapperFactory
	DeferredAwaitings DeferredAwaitingStore
	Proxy             ProxyPort
	ResourceTickets   proxy.TicketIssuer
}
type Service struct {
	deps              Dependencies
	backgroundCtx     context.Context
	deferredAwaitings DeferredAwaitingStore
	ticketService     proxy.TicketIssuer
}

func NewService(deps Dependencies) *Service {
	if deps.Proxy == nil {
		deps.Proxy = unavailableProxy{}
	}
	if deps.BackgroundContext == nil {
		deps.BackgroundContext = context.Background()
	}
	if deps.DeferredAwaitings == nil {
		deps.DeferredAwaitings = runstate.NewDeferredAwaitingStore()
	}
	if deps.References == nil {
		deps.References = reference.New(deps.Chats)
	}
	return &Service{deps: deps, backgroundCtx: deps.BackgroundContext, deferredAwaitings: deps.DeferredAwaitings, ticketService: deps.ResourceTickets}
}
func (s *Service) Reconcile() error { return s.hydrateDeferredAwaitings() }
func (s *Service) AttachRun(_ context.Context, ref runtimetypes.RunRef, afterSeq int64) (*runtimetypes.Subscription, error) {
	if s == nil || s.deps.Runs == nil {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(ref.RunID) == "" || afterSeq < 0 {
		return nil, apperrors.New(apperrors.CodeInvalidRequest, "runId is required and afterSeq must not be negative")
	}
	observer, err := s.deps.Runs.AttachObserver(ref.RunID, afterSeq)
	if err != nil {
		return nil, err
	}
	return runtimetypes.NewSubscription(observer.ID, observer.Events, func() {
		s.deps.Runs.DetachObserver(ref.RunID, observer.ID)
		// FreezeAndWait removes live observers from the bus before closing their
		// channels, so DetachObserver alone can no longer find the observer to
		// acknowledge delivery completion. Keep the acknowledgement on the
		// subscription boundary just like the legacy transport adapters did.
		observer.MarkDone()
	}), nil
}
func (s *Service) RunStatus(runID string) (contracts.RunSnapshot, error) {
	if s == nil || s.deps.Runs == nil {
		return contracts.RunSnapshot{}, ErrNotConfigured
	}
	return runstate.Snapshot(s.deps.Runs, s.deps.Chats, runID)
}
