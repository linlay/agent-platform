package query

import (
	"context"
	"encoding/json"
	"errors"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/runtime/acp"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func (s *Service) prepareACP(ctx context.Context, prepared preparedQuery, taskID string) (preparedQuery, error) {
	if s.deps.ACP == nil {
		return preparedQuery{}, acpRequestError(agentengine.Error("engine_unavailable"))
	}
	var p dto.PreparedSession
	var err error
	if taskID != "" {
		p, err = s.deps.ACP.PrepareChild(ctx, prepared.Req.ChatID, prepared.Req.RunID, taskID, prepared.AgentDef)
	} else {
		p, err = s.deps.ACP.Prepare(ctx, prepared.Req.ChatID)
	}
	if err != nil {
		return preparedQuery{}, acpRequestError(err)
	}
	preparedOK := false
	childChatID := p.ChatID
	defer func() {
		if taskID != "" && !preparedOK {
			s.deps.ACP.CloseChat(childChatID)
		}
	}()
	if model := prepared.Req.Model; model != nil {
		selections := []dto.ConfigSelection{}
		for _, wanted := range []struct{ category, value string }{{"model", model.Key}, {"thought_level", model.ReasoningEffort}} {
			if wanted.value == "" {
				continue
			}
			id := ""
			for _, raw := range p.ConfigOptions {
				var option struct{ ID, Category string }
				json.Unmarshal(raw, &option)
				if option.Category == wanted.category {
					id = option.ID
					break
				}
			}
			if id == "" {
				return preparedQuery{}, acpRequestError(agentengine.Error("invalid_config_selection"))
			}
			value, _ := json.Marshal(wanted.value)
			selections = append(selections, dto.ConfigSelection{ConfigID: id, Value: value})
		}
		if len(selections) > 0 {
			p, err = s.deps.ACP.Configure(ctx, dto.ConfigUpdate{ChatID: p.ChatID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision, Selections: selections})
			if err != nil {
				return preparedQuery{}, acpRequestError(err)
			}
		}
	}
	req, def := prepared.Req, prepared.AgentDef
	prepared.Session = contracts.QuerySession{
		BackendSession: &contracts.BackendSession{ChatID: p.ChatID, EngineID: p.EngineID, PreparationID: p.PreparationID, ConfigRevision: p.ConfigRevision}, SubTaskID: taskID, RequestID: req.RequestID, RunID: req.RunID, ChatID: req.ChatID, ChatName: prepared.Summary.ChatName,
		AgentKey: req.AgentKey, RunOwner: contracts.AgentRunOwner(req.AgentKey), AgentName: def.Name, Mode: def.Mode, Locale: req.Locale,
		AccessLevel: req.AccessLevel, WorkspaceRoot: def.Workspace.ProjectDir(), ToolSetFrozen: true, Created: prepared.Created,
		Detail: stream.ResolveDetailLevel(req.Detail, def.Interaction().DefaultDetail),
	}
	interaction := def.Interaction()
	interaction.AccessLevel = false
	prepared.Session.InteractionConfig = &interaction
	if identity := runtimetypes.IdentityFromContext(ctx); identity != nil {
		prepared.Session.Subject = identity.Subject
	}
	if err := s.deps.Sessions.PrepareExternalContext(ctx, req, def, &prepared.Session); err != nil {
		return preparedQuery{}, err
	}
	if err := s.deps.ACP.ValidateContext(prepared.Session); err != nil {
		return preparedQuery{}, acpRequestError(err)
	}
	preparedOK = true
	return prepared, nil
}

// Stream dispatches the common Run executor without giving ACP a Native tool loop.
func (s *Service) Stream(ctx context.Context, req runtimetypes.QueryCommand, session contracts.QuerySession) (contracts.AgentStream, error) {
	if session.BackendSession != nil {
		return s.deps.ACP.Stream(ctx, req, session, s.deps.Tools, s.deps.ConnectorOperations)
	}
	return s.deps.Agent.Stream(ctx, req, session)
}

func acpRequestError(err error) *statusError {
	code := agentengine.Error("engine_unavailable")
	errors.As(err, &code)
	if errors.Is(err, acp.ErrBusy) {
		code = "engine_busy"
	}
	status := 400
	switch code {
	case "engine_unavailable", "registry_not_ready", "dependency_missing", "rpc_connection_lost":
		status = 503
	case "auth_required":
		status = 401
	case "session_restore_failed", "session_configuration_stale", "engine_busy":
		status = 409
	}
	return &statusError{Status: status, Code: string(code), Message: string(code), Data: map[string]any{"error": map[string]any{"code": string(code), "message": string(code), "executionState": "not_started"}}}
}
func validateACPInput(req runtimetypes.QueryCommand) error {
	// Native elevated access levels have no verified external mapping.
	// ask_for_approval is also the resolved platform default for child/chat_start.
	if len(req.Params) > 0 || req.Scene != nil || req.PlanningMode != nil || req.EditingMode != nil || (req.AccessLevel != "" && req.AccessLevel != contracts.AccessLevelDefault && req.AccessLevel != contracts.AccessLevelAskForApproval) {
		return acpRequestError(agentengine.Error("capability_unsupported"))
	}
	for _, ref := range req.References {
		if ref.Type != "file" && ref.Type != "skill" && ref.Type != "kbase" {
			return acpRequestError(agentengine.Error("capability_unsupported"))
		}
	}
	if req.ResultContract != nil {
		return executeRequestError(400, "execute_unsupported_agent", "only native Agents can be executed", "")
	}
	return nil
}

// BuildQuerySession is also used by the existing child-task coordinator.
func (s *Service) BuildQuerySession(ctx context.Context, req runtimetypes.QueryCommand, summary chat.Summary, def catalog.AgentDefinition, options session.Options) (contracts.QuerySession, error) {
	if catalog.ExecutionBackend(def) != catalog.AgentEngineACP {
		return s.deps.Sessions.BuildQuerySession(ctx, req, summary, def, options)
	}
	if err := validateACPInput(req); err != nil {
		return contracts.QuerySession{}, err
	}
	if options.SubTaskID == "" {
		return contracts.QuerySession{}, acpRequestError(agentengine.Error("capability_unsupported"))
	}
	if options.Identity != nil {
		ctx = runtimetypes.WithIdentity(ctx, options.Identity)
	}
	req.Locale = options.Locale
	prepared, err := s.prepareACP(ctx, preparedQuery{Req: req, Summary: summary, AgentDef: def}, options.SubTaskID)
	return prepared.Session, err
}
func (s *Service) prepareSystemInit(req runtimetypes.QueryCommand, session *contracts.QuerySession, created bool) (*chat.QueryLineSystem, error) {
	if session.BackendSession != nil {
		return nil, nil
	}
	return s.deps.Sessions.PrepareSystemInitCache(req, session, created)
}

// Session ownership is persisted separately from the public event protocol.
func (s *Service) hasEngineSession(chatID string) (bool, error) {
	store, ok := s.deps.Chats.(interface {
		LoadEngineSession(string) (*dto.SessionBinding, error)
	})
	if !ok {
		return false, nil
	}
	binding, err := store.LoadEngineSession(chatID)
	return binding != nil, err
}

// Child backends use transient sessions; their existing task identity resolves
// the backend without adding execution-protocol fields to awaiting events.
func (s *Service) hasEngineAwaiting(chatID string, step *chat.PersistedAwaitingStep) (bool, error) {
	if step != nil && step.TaskAgentKey != "" && s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(step.TaskAgentKey); ok {
			return catalog.ExecutionBackend(def) == catalog.AgentEngineACP, nil
		}
	}
	return s.hasEngineSession(chatID)
}
