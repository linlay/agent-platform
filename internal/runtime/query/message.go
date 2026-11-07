package query

import (
	"context"
	"fmt"
	"sync"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (s *Service) acceptMessage(ctx context.Context, cmd runtimetypes.QueryCommand) (mailbox.Receipt, error) {
	if s.mailbox == nil || s.deps.Registry == nil || s.deps.Chats == nil {
		return mailbox.Receipt{}, ErrNotConfigured
	}
	if cmd.AgentKey == "" {
		cmd.AgentKey = s.deps.Registry.DefaultAgentKey()
	}
	if cmd.Caller.Subject == "" {
		cmd.Caller.Subject = querySourceUser(ctx, cmd)
	}
	if cmd.ChatSource == "" {
		cmd.ChatSource = runtimetypes.ChatSourceFromContext(ctx)
	}
	if cmd.Lane == "main" {
		cmd.Lane = ""
	}
	cmd.MessageRunID = cmd.RunID
	requestID := cmd.RequestID
	if requestID == "" {
		requestID = cmd.RunID
	}
	if requestID == "" {
		requestID = chat.NewRunID()
	}
	if cmd.Origin != nil && cmd.Origin.Type == queryinput.OriginAutomation && cmd.AccessLevel == "" {
		cmd.AccessLevel = contracts.AccessLevelAskForApproval
	}
	return s.Send(ctx, queryinput.SendRequest{RequestID: requestID, ChatID: cmd.ChatID, AgentKey: cmd.AgentKey, Primary: cmd.Primary, Message: cmd.Message, References: cmd.References, PlanningMode: cmd.PlanningMode, EditingMode: cmd.EditingMode, AccessLevel: cmd.AccessLevel, Model: cmd.Model}, cmd)
}
func (s *Service) awaitMessageRun(ctx context.Context, receipt mailbox.Receipt, delivery *messageDelivery) (runtimetypes.RunHandle, error) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	ready := delivery.ready
	for {
		if err := delivery.failure(); err != nil {
			return runtimetypes.RunHandle{}, err
		}
		if handle := delivery.started(); handle != nil {
			return *handle, nil
		}
		m, err := s.mailbox.Store.Get(receipt.MailID)
		if err != nil {
			return runtimetypes.RunHandle{}, err
		}
		if m.Status == mailbox.Failed || m.Status == mailbox.Withdrawn || m.Status == mailbox.Discarded {
			return runtimetypes.RunHandle{}, &statusError{Status: 400, Code: "message_delivery_failed", Message: fmt.Sprint(m.Error)}
		}
		if m.Delivery != "" && m.RunID != "" && (m.Status == mailbox.Delivered || m.Status == mailbox.Done) {
			status, exists := s.deps.Runs.RunStatus(m.RunID)
			if exists {
				return runtimetypes.RunHandle{ChatID: m.ToChatID, RunID: m.RunID, AgentKey: m.ToAgentKey, StartedAt: status.StartedAt, Status: "running", Detached: true, Detail: status.Detail}, nil
			}
		}
		select {
		case <-ctx.Done():
			return runtimetypes.RunHandle{}, ctx.Err()
		case <-tick.C:
		case <-ready:
		}
	}
}
func (s *Service) StartMessage(ctx context.Context, cmd runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	if s.mailbox == nil || s.deps.Registry == nil || s.deps.Chats == nil {
		return runtimetypes.RunHandle{}, ErrNotConfigured
	}
	if cmd.AgentKey == "" {
		cmd.AgentKey = s.deps.Registry.DefaultAgentKey()
	}
	if def, ok := s.deps.Registry.AgentDefinition(cmd.AgentKey); ok && def.Mode == "BOT" {
		summary, err := s.deps.Chats.Summary(cmd.ChatID)
		if err != nil {
			return runtimetypes.RunHandle{}, err
		}
		if summary == nil || chat.IsPendingUploadChat(*summary) {
			return runtimetypes.RunHandle{}, &statusError{Status: 400, Code: "invalid_request", Message: "Use /api/send to open the BOT main Chat"}
		}
	}
	cmd, delivery := s.trackMessage(cmd, !cmd.Detached)
	receipt, err := s.acceptMessage(ctx, cmd)
	if err != nil {
		delivery.abandon()
		return runtimetypes.RunHandle{}, err
	}
	handle, err := s.awaitMessageRun(ctx, receipt, delivery)
	if err != nil {
		delivery.abandon()
		return runtimetypes.RunHandle{}, err
	}
	handle.Subscription = delivery.subscription()
	return handle, nil
}
func (s *Service) ExecuteMessage(ctx context.Context, cmd runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	cmd, delivery := s.trackMessage(cmd, true)
	defer delivery.abandon()
	receipt, err := s.acceptMessage(ctx, cmd)
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	ctx = context.WithoutCancel(ctx)
	handle, err := s.awaitMessageRun(ctx, receipt, delivery)
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	handle.Subscription = delivery.subscription()
	return s.waitMessageResult(ctx, cmd, handle, hooks)
}
func (s *Service) waitMessageResult(ctx context.Context, cmd runtimetypes.QueryCommand, handle runtimetypes.RunHandle, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	if hooks.OnRunStarted != nil {
		hooks.OnRunStarted(chat.RunStart{ChatID: handle.ChatID, RunID: handle.RunID, AgentKey: handle.AgentKey, StartedAtMillis: handle.StartedAt})
	}
	subscription := handle.Subscription
	var err error
	if subscription == nil {
		subscription, err = s.AttachRun(ctx, runtimetypes.RunRef{ChatID: handle.ChatID, RunID: handle.RunID, AgentKey: handle.AgentKey}, 0)
	}
	if err != nil {
		return runtimetypes.QueryResult{}, err
	}
	full := newQueryFullTextBuilder()
	var errorPayload map[string]any
	errorMessage := ""
	for {
		select {
		case <-ctx.Done():
			subscription.Close()
			return runtimetypes.QueryResult{}, ctx.Err()
		case event, open := <-subscription.Events:
			if open {
				if event.Type == "run.error" {
					if payload, ok := event.Payload["error"].(map[string]any); ok {
						errorPayload = contracts.CloneMap(payload)
						errorMessage = contracts.AnyStringNode(payload["message"])
					}
				}
				if cmd.IncludeFullText {
					full.Observe(event)
				}
				continue
			}
			subscription.Close()
			// Persistence follows terminal streaming; wait for the authoritative row.
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				status, exists := s.deps.Runs.RunStatus(handle.RunID)
				if !exists || status.CompletedAt == 0 {
					select {
					case <-ctx.Done():
						return runtimetypes.QueryResult{}, ctx.Err()
					case <-tick.C:
					}
					continue
				}
				rows, e := s.deps.Chats.ListRuns(handle.ChatID)
				if e != nil {
					return runtimetypes.QueryResult{}, e
				}
				for _, r := range rows {
					if r.RunID == handle.RunID && r.CompletedAt > 0 {
						completion := &chat.RunCompletion{ChatID: r.ChatID, RunID: r.RunID, AgentKey: r.AgentKey, AgentMode: r.AgentMode, InitialMessage: r.InitialMessage, AssistantText: r.AssistantText, FinishReason: r.FinishReason, StartedAtMillis: r.StartedAt, UpdatedAtMillis: r.CompletedAt, Usage: r.Usage}
						result := runtimetypes.QueryResult{Persisted: true, Completion: completion, ChatID: r.ChatID, RunID: r.RunID, Content: r.AssistantText, FinishReason: r.FinishReason, Usage: r.Usage, ErrorPayload: errorPayload, ErrorMessage: errorMessage}
						if cmd.IncludeFullText {
							result.FullText = full.Text(r.AssistantText)
						}
						return result, nil
					}
				}
				return runtimetypes.QueryResult{ChatID: handle.ChatID, RunID: handle.RunID, ErrorMessage: "Run completion was not persisted"}, fmt.Errorf("Run completion was not persisted")
			}
		}
	}
}

func (s *Service) MessageSteer(ctx context.Context, cmd runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	if err := s.ValidateRunOwner(cmd.RunID, cmd.AgentKey); err != nil {
		return runtimetypes.SteerResult{}, err
	}
	status, ok := s.deps.Runs.RunStatus(cmd.RunID)
	if !ok || status.CompletedAt != 0 {
		return s.Steer(ctx, cmd)
	}
	if bus, exists := s.deps.Runs.EventBus(cmd.RunID); exists && bus.IsFrozen() {
		return runtimetypes.SteerResult{Accepted: false, Status: "unmatched", RunID: cmd.RunID, SteerID: cmd.SteerID}, nil
	}
	if owner, exists := s.deps.Runs.(interface {
		RunControlScope(string) (controlscope.Scope, bool)
	}); exists {
		if scope, found := owner.RunControlScope(cmd.RunID); found && scope.Lane != "" && scope.Lane != "main" {
			return s.Steer(ctx, cmd)
		}
	}
	if def, exists := s.deps.Registry.AgentDefinition(status.AgentKey); exists {
		main, err := s.primaryChatRole(def, status.ChatID, false)
		if err != nil {
			return runtimetypes.SteerResult{}, err
		}
		if main {
			return runtimetypes.SteerResult{}, &statusError{Status: 400, Code: "bot_chat_queue_only", Message: "Use /api/send to queue messages for the BOT main Chat"}
		}
	}
	if cmd.ChatID != "" && cmd.ChatID != status.ChatID {
		return runtimetypes.SteerResult{}, &statusError{Status: 400, Message: "chatId does not match run"}
	}
	id := cmd.RequestID
	if id == "" {
		id = cmd.SteerID
	}
	if id == "" {
		id = chat.NewRunID()
	}
	receipt, err := s.Send(ctx, queryinput.SendRequest{RequestID: id, ChatID: status.ChatID, AgentKey: status.AgentKey, Message: cmd.Message, References: cmd.References}, runtimetypes.QueryCommand{Caller: runtimetypes.Caller{Subject: controlscope.FromContext(ctx).Subject}})
	if err != nil {
		return runtimetypes.SteerResult{}, err
	}
	ackStatus := "accepted"
	if receipt.Delivery == "queued" {
		ackStatus = "queued"
	}
	return runtimetypes.SteerResult{Accepted: true, Status: ackStatus, RunID: receipt.RunID, SteerID: receipt.MailID}, nil
}

type messageDelivery struct {
	sync.Mutex
	err       error
	sub       *runtimetypes.Subscription
	abandoned bool
	handle    *runtimetypes.RunHandle
	ready     chan struct{}
}

func (d *messageDelivery) started() *runtimetypes.RunHandle {
	d.Lock()
	defer d.Unlock()
	return d.handle
}
func (d *messageDelivery) wake() {
	select {
	case d.ready <- struct{}{}:
	default:
	}
}
func (d *messageDelivery) failure() error { d.Lock(); defer d.Unlock(); return d.err }
func (d *messageDelivery) subscription() *runtimetypes.Subscription {
	d.Lock()
	defer d.Unlock()
	return d.sub
}
func (d *messageDelivery) abandon() {
	d.Lock()
	d.abandoned = true
	sub := d.sub
	d.Unlock()
	if sub != nil {
		sub.Close()
	}
}
func (s *Service) trackMessage(cmd runtimetypes.QueryCommand, observe bool) (runtimetypes.QueryCommand, *messageDelivery) {
	d := &messageDelivery{ready: make(chan struct{}, 1)}
	cmd.MessageDispatched = func(handle runtimetypes.RunHandle) { d.Lock(); d.handle = &handle; d.Unlock(); d.wake() }
	cmd.MessageDispatchError = func(err error) { d.Lock(); d.err = err; d.Unlock(); d.wake() }
	if observe {
		cmd.ObserveMessageRun = func(handle runtimetypes.RunHandle) (func(), error) {
			d.Lock()
			defer d.Unlock()
			if d.abandoned {
				return nil, nil
			}
			sub, err := s.AttachRun(context.Background(), runtimetypes.RunRef{ChatID: handle.ChatID, RunID: handle.RunID, AgentKey: handle.AgentKey}, 0)
			if err != nil {
				return nil, err
			}
			d.sub = sub
			return d.sub.Close, nil
		}
	}
	return cmd, d
}
