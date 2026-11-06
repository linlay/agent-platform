package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	sdk "github.com/coder/acp-go-sdk"
)

// Stream adapts one external turn to the existing Run event pipeline.
func (s *Sessions) Stream(ctx context.Context, req runtimetypes.QueryCommand, session contracts.QuerySession, tools contracts.ToolExecutor, connectors *connectorops.Service) (contracts.AgentStream, error) {
	p := session.BackendSession
	if p == nil {
		return nil, agentengine.Error("session_configuration_stale")
	}
	content, err := s.promptContent(req, p.ChatID)
	if err != nil {
		return nil, err
	}
	for _, prompt := range session.CapabilityPrompts {
		content = append(content, sdk.TextBlock(prompt))
	}
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }
	out := &queryStream{runID: req.RunID, ctx: ctx, cancel: cancel, items: make(chan contracts.AgentDelta, 32), done: make(chan struct{})}
	ctx = context.WithValue(ctx, platformToolsKey{}, &platformTools{executor: tools, connectors: connectors, session: session, request: req, emit: out.send})
	if session.SubTaskID != "" {
		out.release = func() { s.CloseChat(p.ChatID) }
	}
	go func() {
		defer close(out.done)
		defer close(out.items)
		contentGuard := stream.NewMarkdownDestinationGuard(req.ChatID)
		contentGuard.RewriteDestination = func(destination string) string { return workspaceDestination(session.WorkspaceRoot, destination) }
		events := turnEvents{content: contentGuard, chatID: req.ChatID, runID: req.RunID, tools: map[string]*observedTool{}, emit: out.send}
		// Child plans remain task-scoped live updates, as in Native execution.
		if session.SubTaskID == "" {
			events.chatsDir = filepath.Dir(s.chats.ChatDir(req.ChatID))
		}
		raw, err := s.Prompt(ctx, p.ChatID, p.PreparationID, p.ConfigRevision, content, func(ctx context.Context, in Incoming) (any, *sdk.RequestError) {
			if len(in.ID) > 0 {
				// Reverse RPC contexts retain the Run's control, not the transport's identity.
				ctx = contracts.WithRunControl(ctx, contracts.RunControlFromContext(out.ctx))
				result, err := externalInteraction(ctx, in, req, session, out.send)
				if err != nil {
					// Transport/turn teardown is a fault unless the caller cancelled this Run.
					if out.ctx.Err() == nil {
						if errors.Is(err, context.DeadlineExceeded) {
							err = agentengine.Error("awaiting_expired")
						} else if errors.Is(err, context.Canceled) {
							err = agentengine.Error("rpc_connection_lost")
						}
					}
					cancelCause(err)
					return nil, sdk.NewRequestCancelled(nil)
				}
				return result, nil
			}
			if in.Method == "session/update" {
				if err := events.update(in.Params); err != nil {
					cancelCause(err)
					return nil, sdk.NewInvalidParams(nil)
				}
			}
			return nil, nil
		})
		events.finish()
		if out.ctx.Err() != nil {
			return
		}
		if err != nil {
			out.send(callFailure(err))
			return
		}
		var response struct {
			StopReason string
			Usage      map[string]json.RawMessage
		}
		json.Unmarshal(raw, &response)
		// Only a complete, nonnegative per-prompt token report can map to the
		// existing usage contract. Session totals/currency observations cannot.
		counts := map[string]int{}
		complete := true
		for _, key := range []string{"inputTokens", "outputTokens", "totalTokens", "thoughtTokens", "cachedReadTokens"} {
			var count *int
			if json.Unmarshal(response.Usage[key], &count) != nil || count == nil || *count < 0 {
				complete = false
				break
			}
			counts[key] = *count
		}
		if complete {
			out.send(contracts.DeltaUsageSnapshot{ChatID: req.ChatID,
				LLMReturnPromptTokens: counts["inputTokens"], LLMReturnCompletionTokens: counts["outputTokens"], LLMReturnTotalTokens: counts["totalTokens"],
				LLMReturnReasoningTokens: counts["thoughtTokens"], LLMReturnPromptCacheHitTokens: counts["cachedReadTokens"],
				RunPromptTokens: counts["inputTokens"], RunCompletionTokens: counts["outputTokens"], RunTotalTokens: counts["totalTokens"],
				RunReasoningTokens: counts["thoughtTokens"], RunPromptCacheHitTokens: counts["cachedReadTokens"],
			})
		}

		if response.StopReason == "cancelled" {
			out.send(contracts.DeltaRunCancel{RunID: req.RunID})
		} else {
			out.send(contracts.DeltaFinishReason{Reason: response.StopReason})
		}
	}()
	return out, nil
}

type queryStream struct {
	runID             string
	cancelEmitted     bool
	upstreamCancelled bool
	content           strings.Builder
	release           func()
	ctx               context.Context
	cancel            context.CancelFunc
	items             chan contracts.AgentDelta
	done              chan struct{}
}

func (s *queryStream) send(item contracts.AgentDelta) {
	// Cancellation still needs to persist outstanding tool results before the
	// Run terminal. Next consumes them; Close drains if the consumer leaves early.
	s.items <- item
}
func (s *queryStream) Next() (contracts.AgentDelta, error) {
	if s.upstreamCancelled {
		return nil, contracts.ErrRunInterrupted
	}
	item, ok := <-s.items
	if !ok {
		if s.ctx.Err() != nil {
			return s.cancelDelta()
		}
		return nil, io.EOF
	}
	if _, ok := item.(contracts.DeltaRunCancel); ok {
		s.upstreamCancelled = true
	}
	if content, ok := item.(contracts.DeltaContent); ok {
		s.content.WriteString(content.Text)
	}
	return item, nil
}
func (s *queryStream) cancelDelta() (contracts.AgentDelta, error) {
	if cause := context.Cause(s.ctx); cause != context.Canceled && cause != context.DeadlineExceeded {
		if s.cancelEmitted {
			return nil, io.EOF
		}
		s.cancelEmitted = true
		return callFailure(cause), nil
	}
	if !s.cancelEmitted {
		s.cancelEmitted = true
		return contracts.DeltaRunCancel{RunID: s.runID}, nil
	}
	return nil, contracts.ErrRunInterrupted
}

func (s *queryStream) Close() error {
	s.cancel()
	for range s.items {
	}
	<-s.done
	if s.release != nil {
		s.release()
	}
	return nil
}

func (s *queryStream) FinalAssistantContent() (string, bool)      { return s.content.String(), true }
func (s *queryStream) InjectToolResult(string, string, bool) bool { return false }
