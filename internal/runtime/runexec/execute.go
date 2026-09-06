package runexec

import (
	"context"
	"strings"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/orchestration"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

// ExecuteOptions supplies the existing session, event pipeline and narrow
// application ports. The executor owns the Native run loop; callers own
// registration, transport, observer lifetime and continuation admission.
type ExecuteOptions struct {
	RunCtx               context.Context
	Session              contracts.QuerySession
	StartedAtMillis      int64
	Summary              chat.Summary
	StartStream          func(context.Context) (contracts.AgentStream, error)
	Assembler            *stream.StreamEventAssembler
	Mapper               contracts.StreamDeltaMapper
	Billing              config.BillingConfig
	Models               *models.ModelRegistry
	StepWriter           *chat.StepWriter
	RunControl           *contracts.RunControl
	NewOrchestrator      func(context.Context, func(contracts.AgentDelta), func(...stream.StreamInput)) orchestration.DeltaHandler
	PersistCompletion    func(string, chat.UsageData, string, bool) (bool, chat.RunCompletion)
	OnCompactEvent       func(stream.EventData)
	OnPersistenceFailure func(stream.EventData)
	OnProcessingError    func(error)
	OnEvent              func(stream.EventData)
	// ObserveEvent sees normalized internal events, before client filtering.
	ObserveEvent func(stream.EventData)
	Publish      func(stream.EventData) error
}

// Result is the single final result source for both waiting and detached calls.
// Completion is exactly the record passed to persistence, including failures.
type Result struct {
	Completion   chat.RunCompletion
	Persisted    bool
	Continuation *contracts.DeltaRunContinuation
	ErrorMessage string
	ErrorPayload map[string]any
	Err          error
}

func Execute(params ExecuteOptions) (result Result) {
	var persisted bool
	var completion chat.RunCompletion
	var continuation *contracts.DeltaRunContinuation
	defer func() {
		result.Completion, result.Persisted, result.Continuation = completion, persisted, continuation
	}()
	persistCompletion := func(text string, usage chat.UsageData, reason string, notify bool) (bool, chat.RunCompletion) {
		if params.StepWriter != nil {
			params.StepWriter.Flush()
		}
		return params.PersistCompletion(text, usage, reason, notify)
	}
	if err := timecontract.ValidateEpochMillis(params.StartedAtMillis, "startedAt", "run.executor"); err != nil {
		// This is a local platform failure, so its error event is allowed to use
		// the platform's actual error time.  Crucially, we do not repair the
		// invalid start time or continue the run with a guessed value.
		completion = chat.RunCompletion{
			ChatID:          params.Session.ChatID,
			RunID:           params.Session.RunID,
			FinishReason:    "error",
			StartedAtMillis: params.StartedAtMillis,
			UpdatedAtMillis: time.Now().UnixMilli(),
		}
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		params.OnProcessingError(err)
		result.ErrorMessage = err.Error()
		result.Err = err
		return
	}
	// Bind the assembler to the single timestamp captured by run registration.
	// Bootstrap must never call its own clock for run.start: Desktop compares
	// that event with activeRun.startedAt and the run.started notification.
	if params.Assembler != nil {
		params.Assembler.SetRunStartedAtMillis(params.StartedAtMillis)
	}

	var (
		assistantText strings.Builder
		runUsage      chat.UsageData
		chatUsage     chat.UsageData
	)
	if params.Summary.Usage != nil {
		chatUsage = *params.Summary.Usage
	}
	processor := NewProcessor(ProcessorOptions{
		AssistantText: &assistantText, StepWriter: params.StepWriter,
		Billing: params.Billing, Models: params.Models, ChatUsage: chatUsage, RunUsage: &runUsage,
		AggregateUsageByTask: params.Session.TeamRuntime != nil, RunControl: params.RunControl,
		RunID: params.Session.RunID, ChatID: params.Session.ChatID,
		AgentKey:       contracts.ResolveRunOwner(params.Session.RunOwner).AgentKey,
		OnCompactEvent: params.OnCompactEvent,
	})

	runCtx := params.RunCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancelExecution := context.WithCancel(runCtx)
	defer cancelExecution()
	if params.StepWriter != nil {
		runCtx = chat.WithApprovalSummarySink(runCtx, params.StepWriter.RecordApproval)
	}

	var processingErr error
	publishEmissions := func(emissions []stream.EventEmission) error {
		if processingErr != nil {
			return processingErr
		}
		if len(emissions) == 0 {
			return nil
		}
		for _, emission := range emissions {
			event := emission.Event
			// Storage records the public coverage boundary. Hidden events reuse
			// the latest cursor but never reserve or publish that sequence.
			event.Seq = emission.Cursor
			data, _, err := processor.Consume(event)
			if err != nil {
				processingErr = err
				params.OnPersistenceFailure(data)
				// Stop the producer as soon as the bad event is observed. The
				// subsequent local run.error is platform-owned and is published
				// below instead of repairing this event.
				cancelExecution()
				return err
			}
			if params.ObserveEvent != nil && emission.Normalized {
				params.ObserveEvent(data)
			}
			params.OnEvent(data)
			if emission.Visible && params.Publish != nil {
				if err := params.Publish(data); err != nil {
					processingErr = err
					cancelExecution()
					return err
				}
			}
		}
		return nil
	}
	failProcessing := func(err error) {
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		params.OnProcessingError(err)
		result.ErrorMessage = err.Error()
		result.Err = err
		persisted, completion = persistCompletion(assistantText.String(), runUsage, "error", false)
	}

	if err := publishEmissions(params.Assembler.BootstrapEmissions()); err != nil {
		failProcessing(err)
		return
	}

	agentStream, err := params.StartStream(runCtx)
	if err != nil {
		result.ErrorMessage = err.Error()
		result.ErrorPayload = apperrors.FromError(err, apperrors.CodeStreamFailed, apperrors.WithScope(apperrors.ScopeRun))
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishEmissions(params.Assembler.FailEmissions(err)); publishErr != nil {
			failProcessing(publishErr)
			return
		}
		persisted, completion = persistCompletion(assistantText.String(), runUsage, "error", false)
		return
	}
	defer agentStream.Close()

	emitDelta := func(delta contracts.AgentDelta) {
		if processingErr != nil {
			return
		}
		// The TEAM coordinator is a hidden runtime actor. Its reasoning is part of
		// the routing implementation, not user-visible conversation content.
		// Child-agent reasoning is routed through emitInputs below and remains
		// task-scoped, so this only suppresses the coordinator's own reasoning.
		if params.Session.TeamRuntime != nil {
			if _, ok := delta.(contracts.DeltaReasoning); ok {
				return
			}
		}
		if value, ok := delta.(contracts.DeltaRunContinuation); ok {
			cloned := value
			cloned.Answer = contracts.CloneMap(value.Answer)
			continuation = &cloned
			return
		}
		inputs := params.Mapper.Map(delta)
		for _, input := range inputs {
			if processingErr != nil {
				return
			}
			if content, ok := input.(stream.ContentDelta); ok && params.Session.TeamRuntime != nil {
				content.ActorType = "team"
				content.TeamID = strings.TrimSpace(params.Session.TeamID)
				content.AgentKey = ""
				content.Presentation = "reply"
				input = content
			}
			processor.ApplyModelTurnControl(input)
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishEmissions(params.Assembler.ConsumeEmissions(input)); err != nil {
				return
			}
		}
	}
	emitInputs := func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			if processingErr != nil {
				return
			}
			processor.ApplyModelTurnControl(input)
			if marker, ok := input.(stream.StageMarker); ok && params.StepWriter != nil {
				params.StepWriter.OnStageMarker(marker.Stage)
			}
			if err := publishEmissions(params.Assembler.ConsumeEmissions(input)); err != nil {
				return
			}
		}
	}

	handler := params.NewOrchestrator(runCtx, emitDelta, emitInputs)
	orchestrated, orchestrateErr := orchestration.Run(agentStream, handler)
	streamFailed, streamInterrupted := orchestrated.StreamFailed, orchestrated.StreamInterrupted

	if processingErr != nil {
		failProcessing(processingErr)
		return
	}
	if orchestrateErr != nil {
		result.ErrorMessage = orchestrateErr.Error()
		result.ErrorPayload = apperrors.FromError(orchestrateErr, apperrors.CodeStreamFailed, apperrors.WithScope(apperrors.ScopeRun))
		streamFailed = true
		if params.RunControl != nil {
			params.RunControl.TransitionState(contracts.RunLoopStateFailed)
		}
		if publishErr := publishEmissions(params.Assembler.FailEmissions(orchestrateErr)); publishErr != nil {
			failProcessing(publishErr)
			return
		}
	}

	if payload := processor.TerminalErrorPayload(); len(payload) > 0 {
		result.ErrorPayload = payload
		result.ErrorMessage = strings.TrimSpace(contracts.AnyStringNode(payload["message"]))
	}
	terminalFinishReason := processor.TerminalFinishReason()
	if terminalFinishReason == "error" {
		streamFailed = true
		streamInterrupted = false
	} else if terminalFinishReason == "cancel" {
		streamInterrupted = true
		streamFailed = false
	}
	if streamFailed || streamInterrupted {
		finishReason := "error"
		if streamInterrupted {
			finishReason = "cancel"
		}
		persisted, completion = persistCompletion(assistantText.String(), runUsage, finishReason, false)
		return
	}

	if err := publishEmissions(params.Assembler.CompleteEmissions()); err != nil {
		failProcessing(err)
		return
	}
	persisted, completion = persistCompletion(assistantText.String(), runUsage, "complete", true)

	return
}
