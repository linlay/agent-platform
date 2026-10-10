package query

import (
	"fmt"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
)

func (s *Service) startPreparedLocalRun(prepared preparedQuery, registered registeredQueryRun, eventBus *stream.RunEventBus) {
	runexec.StartNative(s.LocalRunExecutorParams(prepared, registered, eventBus))
}

func (s *Service) LocalRunExecutorParams(
	prepared preparedQuery,
	registered registeredQueryRun,
	eventBus *stream.RunEventBus,
) runexec.NativeOptions {
	execution := s.resolvedQueryExecution(prepared)
	if !execution.HiddenRun {
		s.broadcast("run.started", runStartedPushPayload(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey, registered.StartedAtMillis))
	}
	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(execution.StepLineStore, prepared.Req.ChatID, prepared.Req.RunID, prepared.AgentDef.Mode)
	stepWriter.SetPendingSystemInit(prepared.SystemInitLine)
	stepWriter.SetPendingQueryMessages(prepared.Session.CurrentMessages)
	var onUnreadChanged func(chat.Summary)
	var onContinuation func(contracts.DeltaRunContinuation) (string, error)
	notifications := s.deps.Notifications
	if execution.HiddenRun {
		notifications = nil
	} else {
		onUnreadChanged = func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		}

		onContinuation = func(c contracts.DeltaRunContinuation) (string, error) {
			c.ContinuationState = &awaitingContinuationAdmission{Summary: prepared.Summary, TeamID: prepared.Req.TeamID, AgentKey: prepared.AgentDef.Key, TeamSnapshot: prepared.TeamSnapshot, AgentDef: prepared.AgentDef, Frozen: true}
			return s.startRunContinuation(c)
		}
	}

	return runexec.NativeOptions{
		RunCtx:            registered.RunCtx,
		Request:           prepared.Req,
		Session:           prepared.Session,
		StartedAtMillis:   registered.StartedAtMillis,
		Summary:           prepared.Summary,
		Agent:             s.deps.Agent,
		Registry:          s.deps.Registry,
		TeamSnapshot:      prepared.TeamSnapshot,
		Assembler:         assembler,
		Mapper:            mapper,
		Billing:           s.deps.Config.Billing,
		StepWriter:        stepWriter,
		EventBus:          eventBus,
		Chats:             execution.CompletionStore,
		Models:            s.deps.Models,
		RunControl:        registered.Control,
		ResourceBaseURL:   prepared.ResourceBaseURL,
		ResourceTickets:   s.ticketService,
		BuildQuerySession: s.deps.Sessions.BuildQuerySession,
		PrepareSystemInit: s.deps.Sessions.PrepareSystemInitCache,
		Notifications:     notifications,
		OnUnreadChanged:   onUnreadChanged,
		OnContinuation:    onContinuation,
		Release:           prepared.Release,
		OnComplete: func(completion chat.RunCompletion) {
			s.finishRunConnectorPins(completion.RunID, prepared.Req.ChatID)
			s.FinishRegisteredQuery(prepared, registered)
			if !execution.HiddenRun {
				s.broadcast("run.finished", runFinishedPushPayload(
					completion.RunID,
					prepared.Req.ChatID,
					completion.FinishReason,
					completion.UpdatedAtMillis,
				))
			}
		},
	}
}

type queryRunResult struct {
	AssistantText string
	FinishReason  string
	Usage         chat.UsageData
	FullText      string
	ErrorMessage  string
	ErrorPayload  map[string]any
	Completion    *chat.RunCompletion
}

func (s *Service) executePreparedLocalQuery(prepared preparedQuery, registered registeredQueryRun, emitVisible func(stream.EventData) error, observeEvent func(stream.EventData)) (queryRunResult, error) {
	if registered.Control == nil {
		releaseQuery(prepared.Release)
		s.FinishRegisteredQuery(prepared, registered)
		return queryRunResult{}, fmt.Errorf("run control unavailable")
	}
	registered.Control.SetObserverCount(1)
	defer registered.Control.SetObserverCount(0)
	if registered.RunCtx == nil {
		registered.RunCtx = contracts.WithRunControl(registered.Control.Context(), registered.Control)
	}
	var eventBus *stream.RunEventBus
	if registered.Managed {
		eventBus, _ = s.deps.Runs.EventBus(prepared.Req.RunID)
	}
	params := s.LocalRunExecutorParams(prepared, registered, eventBus)
	params.EmitVisible, params.ObserveEvent = emitVisible, observeEvent
	result := runexec.ExecuteNative(params)
	completion := result.Completion
	return queryRunResult{
		AssistantText: completion.AssistantText, FinishReason: completion.FinishReason,
		Usage: completion.Usage, Completion: &completion,
		ErrorMessage: result.ErrorMessage, ErrorPayload: result.ErrorPayload,
	}, result.Err
}
