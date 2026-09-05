package runexec

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

type UsageCostDecorator struct {
	Models  *models.ModelRegistry
	Billing config.BillingConfig
}

func (d UsageCostDecorator) DecorateCurrentUsage(data *stream.EventData) (chat.UsageData, bool) {
	if data == nil || data.Payload == nil {
		return chat.UsageData{}, false
	}
	usage, _ := data.Payload["usage"].(map[string]any)
	if usage == nil {
		return chat.UsageData{}, false
	}
	current, _ := usage["current"].(map[string]any)
	if current == nil {
		return chat.UsageData{}, false
	}
	currentUsage := UsageDataFromMap(current)
	if modelKey := UsageModelKeyFromEvent(data, current); modelKey != "" {
		currentUsage.ModelKey = modelKey
		current["modelKey"] = modelKey
	}
	currentUsage = d.EstimateForModel(currentUsage)
	if estimated := UsageEstimatedCostFromData(currentUsage); estimated != nil {
		current["estimatedCost"] = estimated
	}
	return currentUsage, true
}

func (d UsageCostDecorator) DecorateDebugLLMReturnUsage(inner map[string]any) (chat.UsageData, bool) {
	if inner == nil {
		return chat.UsageData{}, false
	}
	usage, _ := inner["usage"].(map[string]any)
	if usage == nil {
		return chat.UsageData{}, false
	}
	llmReturnUsage, _ := usage["llmReturnUsage"].(map[string]any)
	if llmReturnUsage == nil {
		return chat.UsageData{}, false
	}
	currentUsage := UsageDataFromMap(llmReturnUsage)
	if modelKey := UsageModelKeyFromDebugData(inner, llmReturnUsage); modelKey != "" {
		currentUsage.ModelKey = modelKey
		llmReturnUsage["modelKey"] = modelKey
	}
	currentUsage = d.EstimateForModel(currentUsage)
	if estimated := UsageEstimatedCostFromData(currentUsage); estimated != nil {
		llmReturnUsage["estimatedCost"] = estimated
	}
	return currentUsage, true
}

func (d UsageCostDecorator) EstimateForModel(usage chat.UsageData) chat.UsageData {
	if d.Models == nil {
		return usage
	}
	modelKey := strings.TrimSpace(usage.ModelKey)
	if modelKey == "" || !UsageHasBillableTokens(usage) {
		return usage
	}
	model, err := d.Models.GetModel(modelKey)
	if err != nil || !ModelPricingEnabled(model.Pricing) {
		return usage
	}
	return EstimateUsageCost(usage, model.Pricing, d.Billing)
}

func UsageModelKeyFromEvent(data *stream.EventData, usage map[string]any) string {
	if data == nil || data.Payload == nil {
		return ""
	}
	usageRoot, _ := data.Payload["usage"].(map[string]any)
	modelNode, _ := data.Payload["model"].(map[string]any)
	return strings.TrimSpace(contracts.FirstNonEmptyString(usage["modelKey"], usageRoot["modelKey"], modelNode["key"]))
}

func UsageModelKeyFromDebugData(inner map[string]any, usage map[string]any) string {
	if inner == nil {
		return ""
	}
	modelNode, _ := inner["model"].(map[string]any)
	return strings.TrimSpace(contracts.FirstNonEmptyString(usage["modelKey"], modelNode["key"]))
}

func UsageHasBillableTokens(usage chat.UsageData) bool {
	return usage.PromptTokens > 0 || usage.CompletionTokens > 0 || usage.TotalTokens > 0 ||
		usage.CachedTokens > 0 || usage.PromptCacheHitTokens > 0 || usage.PromptCacheMissTokens > 0
}

func EstimateUsageCost(usage chat.UsageData, pricing models.ModelPricing, billing config.BillingConfig) chat.UsageData {
	if !ModelPricingEnabled(pricing) {
		return usage
	}
	currency := strings.ToUpper(strings.TrimSpace(pricing.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(billing.Currency))
	}
	if currency == "" {
		currency = "CNY"
	}
	cacheHitTokens := usage.PromptCacheHitTokens
	if cacheHitTokens <= 0 {
		cacheHitTokens = usage.CachedTokens
	}
	cacheMissTokens := usage.PromptCacheMissTokens
	if cacheHitTokens <= 0 && cacheMissTokens <= 0 {
		cacheMissTokens = usage.PromptTokens
	} else if cacheMissTokens <= 0 && usage.PromptTokens > cacheHitTokens {
		cacheMissTokens = usage.PromptTokens - cacheHitTokens
	}
	usage.EstimatedCostCurrency = currency
	usage.EstimatedCostInputHit = float64(cacheHitTokens) * pricing.InputCacheHit / 1_000_000
	usage.EstimatedCostInputMiss = float64(cacheMissTokens) * pricing.InputCacheMiss / 1_000_000
	usage.EstimatedCostOutput = float64(usage.CompletionTokens) * pricing.Output / 1_000_000
	usage.EstimatedCostTotal = usage.EstimatedCostInputHit + usage.EstimatedCostInputMiss + usage.EstimatedCostOutput
	return usage
}

func ModelPricingEnabled(pricing models.ModelPricing) bool {
	return pricing.InputCacheHit > 0 || pricing.InputCacheMiss > 0 || pricing.Output > 0
}
