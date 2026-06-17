package llm

import (
	"errors"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/hitl"
)

type hitlSubmitWaitConfig struct {
	awaitingID          string
	mode                string
	ruleTimeout         int64
	onAccessLevelChange func() (bool, error)
	onTimeout           func()
}

func (s *llmRunStream) awaitHITLSubmitOrAccessLevelChange(config hitlSubmitWaitConfig) (SubmitResult, error) {
	timeout := time.Duration(s.resolveHITLTimeoutWithItem(config.mode, config.ruleTimeout)) * time.Second
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		_, version := s.runControl.AccessLevelSnapshot()
		wait := timeout
		if !deadline.IsZero() {
			wait = time.Until(deadline)
			if wait <= 0 {
				if config.onTimeout != nil {
					config.onTimeout()
				}
				return SubmitResult{Status: "hitl_timeout"}, nil
			}
		}
		submitResult, accessChanged, err := s.runControl.AwaitSubmitWithTimeoutOrAccessLevelChange(s.ctx, config.awaitingID, wait, version)
		if accessChanged {
			if config.onAccessLevelChange == nil {
				continue
			}
			resolved, resolveErr := config.onAccessLevelChange()
			if resolveErr != nil || resolved {
				return SubmitResult{Status: "access_level_auto_approved"}, resolveErr
			}
			continue
		}
		if err != nil {
			if errors.Is(err, ErrRunInterrupted) {
				return SubmitResult{}, s.handleInterruptIfNeeded()
			}
			if config.onTimeout != nil {
				config.onTimeout()
			}
			return SubmitResult{Status: "hitl_timeout"}, nil
		}
		return submitResult, nil
	}
}

func (s *llmRunStream) appendHITLRequestSubmit(awaitingID string, submitResult SubmitResult) {
	s.pending = append(s.pending, DeltaRequestSubmit{
		RequestID:  s.session.RequestID,
		ChatID:     s.session.ChatID,
		RunID:      s.session.RunID,
		AwaitingID: awaitingID,
		SubmitID:   submitResult.Request.SubmitID,
		Params:     submitResult.Request.Params,
	})
}

func (s *llmRunStream) normalizeHITLSubmitAndEmitAnswer(awaitingID string, awaitArgs map[string]any, submitResult SubmitResult) (map[string]any, error) {
	normalized, normalizeErr := s.normalizeHITLSubmit(awaitArgs, submitResult.Request.Params)
	if normalizeErr != nil {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer: awaitingAnswerWithSubmitID(
				AwaitingErrorAnswer(strings.TrimSpace(AnyStringNode(awaitArgs["mode"])), "invalid_submit", normalizeErr.Error()),
				submitResult.Request.SubmitID,
			),
		})
		return nil, normalizeErr
	}
	if len(normalized) > 0 {
		s.pending = append(s.pending, DeltaAwaitingAnswer{
			AwaitingID: awaitingID,
			Answer:     awaitingAnswerWithSubmitID(normalized, submitResult.Request.SubmitID),
		})
	}
	return normalized, nil
}

func (s *llmRunStream) timeoutHITLSubmit(invocation *preparedToolInvocation, match hitl.InterceptResult, awaitingID string, mode string) {
	timeoutSeconds := s.resolveHITLTimeoutWithItem(mode, int64(match.Rule.Timeout))
	s.pending = append(s.pending, DeltaAwaitingAnswer{
		AwaitingID: awaitingID,
		Answer:     hitlTimeoutAnswer(strings.TrimSpace(mode), timeoutSeconds),
	})
	s.applyHITLDecision(invocation, match, awaitingID, "reject", "timeout", false)
	s.appendOriginalToolResult(invocation, hitlTimeoutToolResult(invocation))
}
