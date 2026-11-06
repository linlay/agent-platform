package query

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/mailbox"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
)

type userSend struct {
	done   chan struct{}
	digest string
	result mailbox.Receipt
	err    error
}
type userSends struct {
	sync.Mutex
	pending  map[string]*userSend
	contexts map[string]runtimetypes.QueryCommand
}

func (s *Service) Send(ctx context.Context, request queryinput.SendRequest, command runtimetypes.QueryCommand) (mailbox.Receipt, error) {
	if s.mailbox == nil {
		return mailbox.Receipt{}, ErrNotConfigured
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return mailbox.Receipt{}, &statusError{Status: 400, Code: "invalid_request", Message: "requestId is required"}
	}
	// An empty continuation stays on /api/query; a message may carry only references.
	if strings.TrimSpace(request.Message) == "" && len(request.References) == 0 {
		return mailbox.Receipt{}, &statusError{Status: 400, Code: "invalid_request", Message: "message or references are required"}
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return mailbox.Receipt{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	key := command.Caller.Subject + "\x00" + request.RequestID
	s.sends.Lock()
	if s.sends.pending == nil {
		s.sends.pending = map[string]*userSend{}
		s.sends.contexts = map[string]runtimetypes.QueryCommand{}
	}
	if prior := s.sends.pending[key]; prior != nil {
		s.sends.Unlock()
		if prior.digest != digest {
			return mailbox.Receipt{}, runToolError("idempotency_conflict", "requestId already used with different content")
		}
		select {
		case <-prior.done:
			return prior.result, prior.err
		case <-ctx.Done():
			return mailbox.Receipt{}, ctx.Err()
		}
	}
	entry := &userSend{done: make(chan struct{}), digest: digest}
	s.sends.pending[key] = entry
	s.sends.Unlock()
	entry.result, entry.err = s.acceptUserSend(ctx, request, command, digest)
	s.sends.Lock()
	// Only concurrent duplicates wait here; once accepted, MAILS answers repeats.
	delete(s.sends.pending, key)
	close(entry.done)
	s.sends.Unlock()
	return entry.result, entry.err
}
func (s *Service) acceptUserSend(ctx context.Context, r queryinput.SendRequest, cmd runtimetypes.QueryCommand, digest string) (mailbox.Receipt, error) {
	if old, err := s.mailbox.Store.UserRequest(cmd.Caller.Subject, r.RequestID); err == nil {
		if old.RequestDigest != digest {
			return mailbox.Receipt{}, runToolError("idempotency_conflict", "requestId already used with different content")
		}
		delivery := old.Delivery
		if delivery == "" {
			delivery = "queued"
		}
		return mailbox.Receipt{MailID: old.ID, ChatID: old.ToChatID, Delivery: delivery, RunID: old.RunID, Status: old.Status, IgnoredOptions: old.IgnoredOptions}, nil
	} else if !errors.Is(err, mailbox.ErrNotFound) {
		return mailbox.Receipt{}, err
	}
	cmd.RequestID = r.RequestID
	cmd.ChatID = r.ChatID
	cmd.AgentKey = r.AgentKey
	cmd.Message = r.Message
	cmd.References = r.References
	cmd.PlanningMode = r.PlanningMode
	cmd.EditingMode = r.EditingMode
	cmd.Model = cloneModelOptions(r.Model)
	cmd.AccessLevel = r.AccessLevel
	cmd.Primary = r.Primary
	cmd.MailboxAcceptance = true
	identity := runtimetypes.IdentityFromContext(ctx)
	if identity == nil {
		identity = &contracts.AuthIdentity{Subject: cmd.Caller.Subject}
	}
	frozenIdentity := *identity
	cmd.Identity = &frozenIdentity
	ctx = runtimetypes.WithIdentity(ctx, cmd.Identity)
	summary, err := s.deps.Chats.Summary(cmd.ChatID)
	if cmd.ChatID == "" {
		summary = nil
		err = nil
	}
	if err != nil {
		return mailbox.Receipt{}, err
	}
	if summary == nil || chat.IsPendingUploadChat(*summary) {
		if strings.TrimSpace(cmd.AgentKey) == "" {
			return mailbox.Receipt{}, &statusError{Status: 400, Message: "agentKey is required for a new Chat"}
		}
	} else if !s.mailChatOwned(*summary, cmd.Caller.Subject, map[string]bool{}) {
		return mailbox.Receipt{}, &statusError{Status: 403, Message: "Chat is not owned by this user"}
	}
	if cmd.ChatID != "" && summary == nil {
		if archive, ok := s.deps.PrimaryChats.(interface{ ChatArchived(string) (bool, error) }); ok {
			found, e := archive.ChatArchived(cmd.ChatID)
			if e != nil {
				return mailbox.Receipt{}, e
			}
			if found {
				return mailbox.Receipt{}, &statusError{Status: 404, Message: "Chat is archived"}
			}
		}
	}
	admission, err := s.PrepareQueryAdmissionRequest(ctx, cmd, true, cmd.Locale, cmd.ResourceBaseURL)
	if err != nil {
		return mailbox.Receipt{}, err
	}
	defer releaseQuery(admission.Release)
	if summary != nil && !chat.IsPendingUploadChat(*summary) && summary.AgentKey != admission.Req.AgentKey {
		return mailbox.Receipt{}, &statusError{Status: 409, Code: "target_owner_mismatch", Message: "target Agent does not own Chat"}
	}
	if _, err = s.deps.Sessions.PrepareRunReferences(ctx, contracts.QuerySession{ChatID: admission.Req.ChatID, AgentKey: admission.Req.AgentKey, Subject: cmd.Caller.Subject}, admission.AgentDef, admission.Req.References, false, false, false); err != nil {
		return mailbox.Receipt{}, err
	}
	if _, _, err = s.prepareAdmittedChat(ctx, admission); err != nil {
		return mailbox.Receipt{}, err
	}
	cmd = admission.Req
	cmd.MailboxAcceptance = false
	cmd.Primary = false
	cmd.Identity = &frozenIdentity
	input, _ := json.Marshal(r)
	planning := cmd.PlanningMode != nil && *cmd.PlanningMode
	editing := agentbuiltin.WorkspaceEditingDefault(admission.AgentDef.Mode)
	if cmd.EditingMode != nil {
		editing = *cmd.EditingMode
	}
	cmd.PlanningMode, cmd.EditingMode = &planning, &editing
	m := mailbox.Mail{
		ID: mailbox.NewID("mail"), Kind: mailbox.Letter, Status: mailbox.Queued,
		CreatedAt: nowMillis(), UpdatedAt: nowMillis(),
		ToAgentKey: cmd.AgentKey, ToChatID: cmd.ChatID, OriginType: "user",
		Subject: cmd.Caller.Subject, RequestID: r.RequestID,
		Input: input, Message: r.Message, References: cmd.References,
		Model: cmd.Model, PlanningMode: cmd.PlanningMode, EditingMode: cmd.EditingMode,
		AccessLevel: cmd.AccessLevel, AccessGrant: cmd.AccessGrant,
		RequestDigest: digest,
	}

	// The request's delivery context lives in memory until the mail is
	// consumed or terminal. TODO(chats-db-v2): persist it once queued mail
	// survives restarts.
	s.sends.Lock()
	s.sends.contexts[m.ID] = runtimetypes.QueryCommand{Locale: cmd.Locale, ClientTarget: cmd.ClientTarget, ResourceBaseURL: cmd.ResourceBaseURL, Caller: cmd.Caller, Identity: cmd.Identity}
	s.sends.Unlock()
	receipt, err := s.mailbox.AcceptUser(context.WithoutCancel(ctx), m, controlscope.FromContext(ctx))
	if err != nil || receipt.MailID != m.ID {
		s.MailReleased(m.ID)
	}
	return receipt, err
}

// MailReleased forgets a user mail's delivery context once it can no longer
// open or join a Run.
func (s *Service) MailReleased(mailID string) {
	s.sends.Lock()
	delete(s.sends.contexts, mailID)
	s.sends.Unlock()
}

func cloneModelOptions(options *queryinput.QueryModelOptions) *queryinput.QueryModelOptions {
	if options == nil {
		return nil
	}
	clone := *options
	return &clone
}
func nowMillis() int64 { return time.Now().UnixMilli() }
func (s *Service) userIgnoredOptions(m mailbox.Mail, active contracts.RunStatusInfo) []string {
	var original queryinput.SendRequest
	_ = json.Unmarshal(m.Input, &original)
	var ignored []string
	if original.PlanningMode != nil && *original.PlanningMode != active.PlanningMode {
		ignored = append(ignored, "planningMode")
	}
	if original.EditingMode != nil && *original.EditingMode != active.EditingMode {
		ignored = append(ignored, "editingMode")
	}
	if original.Model != nil {
		var current queryinput.QueryModelOptions
		if def, ok := s.deps.Registry.AgentDefinition(m.ToAgentKey); ok {
			current.Key = def.ModelKey
			current.ReasoningEffort = def.ModelReasoningEffort
			current.ServiceTier = def.ServiceTier
		}
		if q, err := s.deps.Chats.LoadRunQuery(m.ToChatID, active.RunID); err == nil && q != nil && q.Model != nil {
			raw, _ := json.Marshal(q.Model)
			var saved queryinput.QueryModelOptions
			_ = json.Unmarshal(raw, &saved)
			if saved.Key != "" {
				current.Key = saved.Key
			}
			if saved.ReasoningEffort != "" {
				current.ReasoningEffort = saved.ReasoningEffort
			}
			if saved.ServiceTier != "" {
				current.ServiceTier = saved.ServiceTier
			}
		}
		if active.FrozenModelOptions != nil {
			current = *active.FrozenModelOptions
		}
		current.ReasoningEffort, _ = normalizeQueryModelReasoningEffort(current.ReasoningEffort)
		current.ServiceTier, _ = normalizeQueryModelServiceTier(current.ServiceTier)
		requested := current
		explicit := *original.Model
		explicit.Key = strings.TrimSpace(explicit.Key)
		explicit.ReasoningEffort, _ = normalizeQueryModelReasoningEffort(explicit.ReasoningEffort)
		explicit.ServiceTier, _ = normalizeQueryModelServiceTier(explicit.ServiceTier)
		if explicit.Key != "" {
			requested.Key = explicit.Key
		}
		if explicit.ReasoningEffort != "" {
			requested.ReasoningEffort = explicit.ReasoningEffort
		}
		if explicit.ServiceTier != "" {
			requested.ServiceTier = explicit.ServiceTier
		}
		if !reflect.DeepEqual(requested, current) {
			ignored = append(ignored, "model")
		}
	}
	return ignored
}

func freezeModelOptions(def catalog.AgentDefinition, requested *queryinput.QueryModelOptions) *queryinput.QueryModelOptions {
	model := queryinput.QueryModelOptions{Key: def.ModelKey, ReasoningEffort: def.ModelReasoningEffort, ServiceTier: def.ServiceTier}
	if requested != nil {
		if requested.Key != "" {
			model.Key = requested.Key
		}
		if requested.ModelID != "" {
			model.ModelID = requested.ModelID
		}
		if requested.ReasoningEffort != "" {
			model.ReasoningEffort = requested.ReasoningEffort
		}
		if requested.ServiceTier != "" {
			model.ServiceTier = requested.ServiceTier
		}
	}
	return &model
}
