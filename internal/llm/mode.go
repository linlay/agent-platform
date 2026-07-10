package llm

import (
	"context"
	"io"
	"strings"

	agentcoder "agent-platform/internal/agent/coder"
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
	case "KBASE":
		return kbaseMode{}
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
		return agentcoder.NewPlanningStream(engine, ctx, req, session)
	}
	if agentcoder.IsPlanApproveContinuationParams(req.Params) {
		settings := session.ResolvedStageSettings
		executeTools := agentcoder.PlanningExecuteToolsForStage(settings.Execute, session.ToolNames)
		stageSession := session
		stageSession.ToolNames = append([]string(nil), executeTools...)
		if modelKey := strings.TrimSpace(settings.Execute.ModelKey); modelKey != "" {
			stageSession.ModelKey = modelKey
		}
		stream, err := engine.newRunStreamWithOptions(ctx, req, stageSession, true, runStreamOptions{
			ToolNames: executeTools,
			ModelKey:  strings.TrimSpace(settings.Execute.ModelKey),
			Stage:     "coder-execute",
		})
		if err != nil {
			return nil, err
		}
		pending := []AgentDelta{
			DeltaStageMarker{Stage: "coder-execute"},
		}
		if !req.SyntheticQueryBootstrapped {
			pending = append(pending, DeltaSyntheticQuery{
				ChatID:   session.ChatID,
				Role:     api.QueryRoleUser,
				Message:  agentcoder.ExecuteSyntheticQueryMessage(session.Locale),
				Messages: cloneRawMessageMaps(session.CurrentMessages),
				System:   takePendingSystemPayload(&session, "coder:execute"),
			})
		}
		return &prefixedAgentStream{
			pending: pending,
			stream:  stream,
		}, nil
	}
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: "coder",
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

func takePendingSystemPayload(session *QuerySession, cacheKey string) map[string]any {
	if session == nil || !session.PendingSystemInitKeys[cacheKey] {
		return nil
	}
	snapshot, ok := session.SystemInitCache[cacheKey]
	if !ok || strings.TrimSpace(snapshot.Fingerprint) == "" {
		return nil
	}
	delete(session.PendingSystemInitKeys, cacheKey)
	return map[string]any{
		"agentKey":       snapshot.AgentKey,
		"cacheKey":       cacheKey,
		"fingerprint":    snapshot.Fingerprint,
		"systemMessage":  cloneAnyMapViaJSON(snapshot.SystemMessage),
		"tools":          cloneAnySliceViaJSON(snapshot.Tools),
		"model":          cloneAnyMapViaJSON(snapshot.Model),
		"toolChoice":     snapshot.ToolChoice,
		"requestOptions": cloneAnyMapViaJSON(snapshot.RequestOptions),
	}
}

func cloneAnySliceViaJSON(values []any) []any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		if mapped, ok := value.(map[string]any); ok {
			out = append(out, cloneAnyMapViaJSON(mapped))
			continue
		}
		out = append(out, value)
	}
	return out
}

type kbaseMode struct{}

func (kbaseMode) Start(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	return engine.newRunStreamWithOptions(ctx, req, session, true, runStreamOptions{
		Stage: "kbase",
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
