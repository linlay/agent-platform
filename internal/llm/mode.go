package llm

import (
	"context"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type AgentMode interface {
	Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error)
}

func resolveAgentMode(mode string) AgentMode {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "ONESHOT":
		return oneshotMode{}
	case "PLAN_EXECUTE", "PLAN-EXECUTE":
		return planPipelineMode{}
	case "CODER":
		return coderMode{}
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

type coderMode struct{}

func (coderMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	if session.PlanningMode {
		return newCoderPlanningStream(engine, ctx, req, session)
	}
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: "coder",
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
