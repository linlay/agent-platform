package memoryworker

import (
	"context"
	"errors"
	"fmt"
)

// reconcile runs even when no new batches were extracted: yesterday's facts,
// failed publications and manual edits still need reconciliation after restart.
func (w *Worker) reconcile(ctx context.Context, through string) error {
	var catalog struct {
		Agents []struct {
			AgentKey string `json:"agentKey"`
		} `json:"agents"`
	}
	if err := w.call(ctx, "agents", struct{}{}, &catalog); err != nil {
		return fmt.Errorf("list memory agents: %w", err)
	}
	var failures []error
	for _, agent := range catalog.Agents {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		params := map[string]any{"agentKey": agent.AgentKey, "through": through,
			"maxTokens": w.cfg.Summary.Agent.MaxTokens, "maxLines": w.cfg.Summary.Agent.MaxLines}
		if err := w.call(ctx, "summarize", params, nil); err != nil {
			failures = append(failures, fmt.Errorf("summarize memory agent %s: %w", agent.AgentKey, err))
		}
	}
	if ctx.Err() != nil {
		return errors.Join(append(failures, ctx.Err())...)
	}
	params := map[string]any{"through": through,
		"globalMaxTokens": w.cfg.Summary.Global.MaxTokens, "globalMaxLines": w.cfg.Summary.Global.MaxLines,
		"agentMaxTokens": w.cfg.Summary.Agent.MaxTokens, "agentMaxLines": w.cfg.Summary.Agent.MaxLines}
	var result struct {
		AgentErrors []struct {
			AgentKey string `json:"agentKey"`
			Code     string `json:"code"`
		} `json:"agentErrors"`
	}
	if err := w.call(ctx, "consolidate", params, &result); err != nil {
		failures = append(failures, fmt.Errorf("consolidate memory: %w", err))
	}
	for _, failure := range result.AgentErrors {
		failures = append(failures, fmt.Errorf("consolidate memory agent %s: %s", failure.AgentKey, failure.Code))
	}
	return errors.Join(failures...)
}
