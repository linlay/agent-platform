package llm

import (
	"strings"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
)

const (
	runInterruptedAwaitingMessage = "run interrupted while waiting for user approval"
	runInterruptedApprovalOutput  = "tool execution was cancelled before approval"
	runInterruptedExecutionOutput = "tool execution was cancelled before execution"
)

type interruptedPendingInvocation struct {
	invocation     *preparedToolInvocation
	beforeApproval bool
}

// appendInterruptedWaitingResults closes committed tool calls whose execution
// is known not to have started. It deliberately excludes active execution
// batches and ordinary active tool calls because their side-effect state may
// be unknown after interruption.
func (s *llmRunStream) appendInterruptedWaitingResults() {
	if s == nil {
		return
	}

	awaitingID, mode := "", ""
	approvalToolIDs := map[string]bool{}
	pending := make([]interruptedPendingInvocation, 0)

	if batch := s.hitlPendingBatch; batch != nil {
		awaitingID = strings.TrimSpace(batch.awaitingID)
		mode = "approval"
		for _, invocation := range batch.invocations {
			if invocation == nil {
				continue
			}
			approvalToolIDs[strings.TrimSpace(invocation.toolID)] = true
		}
		for _, invocation := range s.queuedToolCalls {
			if invocation == nil {
				continue
			}
			pending = append(pending, interruptedPendingInvocation{
				invocation:     invocation,
				beforeApproval: approvalToolIDs[strings.TrimSpace(invocation.toolID)],
			})
		}
		for _, invocation := range batch.invocations {
			if invocation == nil {
				continue
			}
			pending = append(pending, interruptedPendingInvocation{invocation: invocation, beforeApproval: true})
		}
	} else if invocation := s.hitlPendingCall; invocation != nil {
		awaitingID = strings.TrimSpace(s.hitlAwaitingID)
		mode = strings.TrimSpace(AnyStringNode(s.hitlAwaitArgs["mode"]))
		approvalToolIDs[strings.TrimSpace(invocation.toolID)] = true
		pending = append(pending, interruptedPendingInvocation{invocation: invocation, beforeApproval: true})
	} else if invocation := s.activeToolCall; s.isWaitingInteractionInvocation(invocation) {
		awaitingID = strings.TrimSpace(invocation.toolID)
		mode = strings.TrimSpace(AnyStringNode(invocation.args["mode"]))
		if mode == "" {
			mode = "question"
		}
		pending = append(pending, interruptedPendingInvocation{invocation: invocation})
	}

	if s.hitlPendingBatch == nil {
		for _, invocation := range s.queuedToolCalls {
			if invocation == nil {
				continue
			}
			pending = append(pending, interruptedPendingInvocation{
				invocation:     invocation,
				beforeApproval: approvalToolIDs[strings.TrimSpace(invocation.toolID)],
			})
		}
	}
	if len(pending) == 0 {
		return
	}

	if awaitingID != "" {
		if mode == "" {
			mode = "approval"
		}
		answer := AwaitingErrorAnswer(mode, string(apperrors.CodeRunInterrupted), runInterruptedAwaitingMessage)
		if errorPayload := AnyMapNode(answer["error"]); len(errorPayload) > 0 {
			errorPayload["reason"] = string(apperrors.CodeRunInterrupted)
		}
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     answer,
		})
	}

	seen := map[string]bool{}
	for _, item := range pending {
		invocation := item.invocation
		if invocation == nil {
			continue
		}
		toolID := strings.TrimSpace(invocation.toolID)
		if toolID == "" || seen[toolID] {
			continue
		}
		seen[toolID] = true
		output := runInterruptedExecutionOutput
		if item.beforeApproval {
			output = runInterruptedApprovalOutput
		}
		s.appendOriginalToolResult(invocation, interruptedBeforeExecutionToolResult(invocation, awaitingID, output))
	}

	s.hitlPendingBatch = nil
	s.hitlPendingCall = nil
	s.hitlMatch = nil
	s.hitlAwaitingID = ""
	s.hitlAwaitArgs = nil
	s.queuedToolCalls = nil
	if s.isWaitingInteractionInvocation(s.activeToolCall) {
		s.activeToolCall = nil
	}
}

func (s *llmRunStream) isWaitingInteractionInvocation(invocation *preparedToolInvocation) bool {
	return s != nil && invocation != nil && s.engine != nil && s.isInteractionTool(invocation.toolName)
}

func interruptedBeforeExecutionToolResult(invocation *preparedToolInvocation, awaitingID string, output string) ToolExecutionResult {
	payload := map[string]any{
		"error":    string(apperrors.CodeRunInterrupted),
		"exitCode": -1,
		"output":   strings.TrimSpace(output),
		"executed": false,
	}
	if awaitingID = strings.TrimSpace(awaitingID); awaitingID != "" {
		payload["awaitingId"] = awaitingID
	}
	return ToolExecutionResult{
		Output:     MarshalJSON(payload),
		Structured: CloneMap(payload),
		RawParams:  CloneMap(payload),
		Error:      string(apperrors.CodeRunInterrupted),
		ExitCode:   -1,
	}
}
