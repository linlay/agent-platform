package llm

import (
	"context"
	"io"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	agentkbase "agent-platform/internal/agent/kbase"
	"agent-platform/internal/agent/planmode"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type AgentMode interface {
	Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

func resolveAgentMode(mode string) AgentMode {
	normalized := strings.ToUpper(strings.TrimSpace(mode))
	switch normalized {
	case "ONESHOT":
		return oneshotMode{}
	case "PLAN_EXECUTE", "PLAN-EXECUTE":
		return planPipelineMode{}
	case agentteam.Mode:
		return teamMode{}
	case "GENERAL", "REACT":
		return reactMode{}
	default:
		if descriptor, ok := agentbuiltin.Lookup(normalized); ok {
			return builtinMode{stage: descriptor.MainStage}
		}
		return reactMode{}
	}
}

type reactMode struct{}

func (reactMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStream(ctx, req, session, true)
}

// startAgentMode applies planningMode before the mode-specific start: planning
// is a capability of every ordinary native Agent, not a mode of its own.
func startAgentMode(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	mode := resolveAgentMode(session.Mode)
	if !agentbuiltin.PlanningModeSupported(session.Mode) {
		return mode.Start(engine, ctx, req, session)
	}
	if session.PlanningMode {
		return planmode.NewPlanningStream(planningRuntimeAdapter{engine: engine, mode: session.Mode}, ctx, req, session)
	}
	if !session.ConfirmedPlanRun {
		return mode.Start(engine, ctx, req, session)
	}
	// A Run started from a confirmed plan is an ordinary Run of the same Agent.
	// The session already carries its tool exclusions; only the synthetic
	// "execute planning" query is added when the caller has not written it.
	pending := []AgentDelta(nil)
	if !req.SyntheticQueryBootstrapped {
		pending = append(pending, DeltaSyntheticQuery{
			ChatID:   session.ChatID,
			Role:     api.QueryRoleUser,
			Message:  planmode.ExecuteSyntheticQueryMessage(session.Locale),
			Messages: cloneRawMessageMaps(session.CurrentMessages),
			System:   TakePendingSystemInitPayload(&session, sessionSystemInitCacheKey(session, "")),
		})
	}
	stream, err := mode.Start(engine, ctx, req, session)
	if err != nil {
		return nil, err
	}
	return &prefixedAgentStream{pending: pending, stream: stream}, nil
}

type prefixedAgentStream struct {
	pending []AgentDelta
	stream  AgentStream
}

func (s *prefixedAgentStream) Next() (AgentDelta, error) {
	if s == nil {
		return nil, io.EOF
	}
	if len(s.pending) > 0 {
		next := s.pending[0]
		s.pending = s.pending[1:]
		return next, nil
	}
	if s.stream == nil {
		return nil, io.EOF
	}
	return s.stream.Next()
}

func (s *prefixedAgentStream) Close() error {
	if s == nil || s.stream == nil {
		return nil
	}
	return s.stream.Close()
}

type builtinMode struct{ stage string }

func (m builtinMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	stage := strings.TrimSpace(m.stage)
	if agentkbase.IsMode(session.Mode) {
		stage = agentkbase.RuntimeStage(session.EditingMode)
	}
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: stage,
	})
}

type teamMode struct{}

func (teamMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage:      agentteam.MainStage,
		ToolChoice: "auto",
	})
}

type oneshotMode struct{}

func (oneshotMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	// Java ONESHOT allows tool use with single tool call + retry + second turn for final answer.
	// Go uses the same stream with allowToolUse=true but MaxSteps limited.
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage:    "oneshot",
		MaxSteps: 2, // One tool call round + one final answer turn
	})
}

type planPipelineMode struct{}

func (planPipelineMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return newPlanPipelineStream(engine, ctx, req, session)
}
