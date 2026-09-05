package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
)

// usageCostDecorator is a temporary server-facing adapter for proxy event
// recording. The pricing implementation and model lookup live in runexec.
type usageCostDecorator struct {
	models  *models.ModelRegistry
	billing config.BillingConfig
}

func (d usageCostDecorator) decorateCurrentUsage(data *stream.EventData) (chat.UsageData, bool) {
	return (runexec.UsageCostDecorator{Models: d.models, Billing: d.billing}).DecorateCurrentUsage(data)
}

func (d usageCostDecorator) decorateDebugLLMReturnUsage(inner map[string]any) (chat.UsageData, bool) {
	return (runexec.UsageCostDecorator{Models: d.models, Billing: d.billing}).DecorateDebugLLMReturnUsage(inner)
}

func (d usageCostDecorator) estimateForModel(usage chat.UsageData) chat.UsageData {
	return (runexec.UsageCostDecorator{Models: d.models, Billing: d.billing}).EstimateForModel(usage)
}

func usageModelKeyFromEvent(data *stream.EventData, usage map[string]any) string {
	return runexec.UsageModelKeyFromEvent(data, usage)
}

func usageEstimatedCostFromData(usage chat.UsageData) map[string]any {
	return runexec.UsageEstimatedCostFromData(usage)
}
