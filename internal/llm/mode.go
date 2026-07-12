package llm

import (
	"context"
	"io"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	agentcoder "agent-platform/internal/agent/coder"
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
	case agentcoder.Mode:
		return coderMode{}
	case agentteam.Mode:
		return teamMode{}
	case "REACT":
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

type coderMode struct{}

func (coderMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	if session.PlanningMode {
		return agentcoder.NewPlanningStream(coderRuntimeAdapter{engine: engine}, ctx, req, session)
	}
	if agentcoder.IsPlanningApproveContinuationParams(req.Params) {
		settings := session.ResolvedCoderPlanningSettings
		executeTools := agentcoder.PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
		stageSession := session
		stageSession.ToolNames = append([]string(nil), executeTools...)
		if modelKey := strings.TrimSpace(settings.Execute.ModelKey); modelKey != "" {
			stageSession.ModelKey = modelKey
		}
		stream, err := engine.newRunStreamWithOptions(ctx, req, stageSession, true, runStreamOptions{
			ToolNames: executeTools,
			ModelKey:  strings.TrimSpace(settings.Execute.ModelKey),
			Stage:     agentcoder.ExecuteStage,
		})
		if err != nil {
			return nil, err
		}
		pending := []AgentDelta{
			DeltaStageMarker{Stage: agentcoder.ExecuteStage},
		}
		if !req.SyntheticQueryBootstrapped {
			pending = append(pending, DeltaSyntheticQuery{
				ChatID:   session.ChatID,
				Role:     api.QueryRoleUser,
				Message:  agentcoder.ExecuteSyntheticQueryMessage(session.Locale),
				Messages: cloneRawMessageMaps(session.CurrentMessages),
				System:   TakePendingSystemInitPayload(&session, agentcoder.ExecuteCacheKey),
			})
		}
		return &prefixedAgentStream{
			pending: pending,
			stream:  stream,
		}, nil
	}
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: agentcoder.MainStage,
	})
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
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: strings.TrimSpace(m.stage),
	})
}

type teamMode struct{}

func (teamMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage:      agentteam.MainStage,
		ToolChoice: "required",
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
