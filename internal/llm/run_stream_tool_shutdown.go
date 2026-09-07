package llm

import (
	"context"
	"errors"
	"time"

	. "agent-platform/internal/contracts"
)

const toolShutdownGrace = 2 * time.Second

// appendCanceledExecutionResults uses one deadline for the entire batch, not
// one timeout per tool. It never runs post-tool hooks or replays a tool. Output
// is live-only; completion mailboxes remain readable even when it is dropped.
func (s *llmRunStream) appendCanceledExecutionResults() {
	grace := s.toolShutdownTimeout
	if grace <= 0 {
		grace = toolShutdownGrace
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if execution := s.activeToolExecution; execution != nil {
		execution.cancel()
		result := waitToolCompletion(ctx, execution.completion)
		invocation := execution.invocation
		s.appendInteractionSubmitDeltas(invocation, result)
		s.appendOriginalToolResult(invocation, result)
		appendSourcePublishDelta(&s.pending, s.session, invocation, result)
		appendPublishedArtifactDelta(&s.pending, s.session, invocation, result.Structured["publishedArtifacts"])
		s.activeToolExecution = nil
		s.activeToolCall = nil
		if s.runControl != nil {
			s.runControl.ClearExpectedSubmit(invocation.toolID)
		}
	}
	if batch := s.activeToolBatch; batch != nil {
		if batch.cancel != nil {
			batch.cancel()
		}
		for index, invocation := range batch.invocations {
			if invocation == nil {
				continue
			}
			if index < len(batch.results) && batch.results[index].received {
				s.appendToolResultMessageOrdered(invocation, batch.results[index].result)
				if s.runControl != nil {
					s.runControl.ClearExpectedSubmit(invocation.toolID)
				}
				continue
			}
			var completion *toolCompletion
			if index < len(batch.completions) {
				completion = batch.completions[index]
			}
			result := waitToolCompletion(ctx, completion)
			s.appendInteractionSubmitDeltas(invocation, result)
			internalOnly := false
			if completion != nil {
				select {
				case <-completion.done:
					internalOnly = completion.result.internalOnly
				default:
				}
			}
			result = s.prepareToolResultForPublish(invocation, result)
			s.emitToolResult(invocation, result, internalOnly)
			s.appendToolResultMessageOrdered(invocation, result)
			if !internalOnly {
				appendSourcePublishDelta(&s.pending, s.session, invocation, result)
				appendPublishedArtifactDelta(&s.pending, s.session, invocation, result.Structured["publishedArtifacts"])
			}
			if s.runControl != nil {
				s.runControl.ClearExpectedSubmit(invocation.toolID)
			}
		}
		s.activeToolBatch = nil
	}
}

func waitToolCompletion(ctx context.Context, completion *toolCompletion) ToolExecutionResult {
	if completion != nil {
		select {
		case <-completion.done:
			return completedToolResult(completion.result)
		case <-ctx.Done():
		}
		// Prefer an actual return already available at the deadline boundary.
		select {
		case <-completion.done:
			return completedToolResult(completion.result)
		default:
		}
	}
	return unknownToolOutcome("cancellation_cleanup_timeout")
}

func completedToolResult(completed batchToolCallResult) ToolExecutionResult {
	if completed.err == nil {
		return completed.result
	}
	if errors.Is(completed.err, context.Canceled) || errors.Is(completed.err, context.DeadlineExceeded) || errors.Is(completed.err, ErrRunInterrupted) {
		result := unknownToolOutcome("executor_canceled")
		result.Structured["cause"] = completed.err.Error()
		if completed.result.Output != "" || len(completed.result.Structured) > 0 {
			result.Structured["partialResult"] = completed.result
		}
		result.Output = MarshalJSON(result.Structured)
		return result
	}
	return ToolExecutionResult{Output: completed.err.Error(), Error: "tool_execution_failed", ExitCode: -1}
}

// This is a platform cancellation observation, not a claimed tool return. In
// particular, omit executed:false: cancellation cannot prove lack of effects.
func unknownToolOutcome(reason string) ToolExecutionResult {
	payload := map[string]any{
		"error": "tool_execution_outcome_unknown", "exitCode": -1,
		"output":         "Tool execution was interrupted or timed out. The final outcome and side effects are unknown; verify external state before retrying.",
		"executionState": "unknown", "reason": reason,
	}
	return ToolExecutionResult{Output: MarshalJSON(payload), Structured: payload, Error: "tool_execution_outcome_unknown", ExitCode: -1}
}
