package engine

import (
	"context"
	"io"
	"strings"

	"agent-platform-runner-go/internal/api"
)

type AgentMode interface {
	Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

func resolveAgentMode(mode string) AgentMode {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ONESHOT":
		return oneshotMode{}
	case "PLAN_EXECUTE":
		return planExecuteMode{}
	case "REACT":
		fallthrough
	default:
		return reactMode{}
	}
}

type reactMode struct{}

func (reactMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStream(ctx, req, session, true)
}

type oneshotMode struct{}

func (oneshotMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStream(ctx, req, session, false)
}

type planExecuteMode struct{}

func (planExecuteMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	base, err := engine.newRunStream(ctx, req, session, true)
	if err != nil {
		return nil, err
	}
	taskID := session.RunID + "_task_1"
	return &planExecuteStream{
		inner: base,
		pending: []AgentDelta{
			DeltaPlanUpdate{
				PlanID: session.RunID + "_plan",
				ChatID: session.ChatID,
				Plan: map[string]any{
					"tasks": []map[string]any{
						{
							"taskId":      taskID,
							"description": req.Message,
							"status":      "in_progress",
						},
					},
				},
			},
			DeltaTaskLifecycle{
				Kind:        "start",
				TaskID:      taskID,
				RunID:       session.RunID,
				TaskName:    "primary_task",
				Description: req.Message,
			},
		},
		taskID: taskID,
	}, nil
}

type planExecuteStream struct {
	inner     AgentStream
	pending   []AgentDelta
	taskID    string
	completed bool
	closed    bool
}

func (s *planExecuteStream) Next() (AgentDelta, error) {
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		return event, nil
	}
	if s.completed {
		return nil, io.EOF
	}
	event, err := s.inner.Next()
	if err == io.EOF {
		s.completed = true
		s.pending = append(s.pending, DeltaTaskLifecycle{
			Kind:   "complete",
			TaskID: s.taskID,
			RunID:  "",
		})
		return s.Next()
	}
	if err != nil {
		return nil, err
	}
	switch value := event.(type) {
	case DeltaRunCancel:
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: s.taskID})
	case DeltaError:
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "fail", TaskID: s.taskID, Error: value.Error})
	}
	return event, nil
}

func (s *planExecuteStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.inner != nil {
		return s.inner.Close()
	}
	return nil
}
