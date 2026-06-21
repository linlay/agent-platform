package server

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

type usageCostDecorator struct {
	models  *models.ModelRegistry
	billing config.BillingConfig
}

func (d usageCostDecorator) decorateCurrentUsage(data *stream.EventData) (chat.UsageData, bool) {
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
	currentUsage := usageDataFromMap(current)
	if modelKey := usageModelKeyFromEvent(data, current); modelKey != "" {
		currentUsage.ModelKey = modelKey
		current["modelKey"] = modelKey
	}
	currentUsage = d.estimateForModel(currentUsage)
	if estimated := usageEstimatedCostFromData(currentUsage); estimated != nil {
		current["estimatedCost"] = estimated
	}
	return currentUsage, true
}

func (d usageCostDecorator) decorateDebugLLMReturnUsage(inner map[string]any) (chat.UsageData, bool) {
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
	currentUsage := usageDataFromMap(llmReturnUsage)
	if modelKey := usageModelKeyFromDebugData(inner, llmReturnUsage); modelKey != "" {
		currentUsage.ModelKey = modelKey
		llmReturnUsage["modelKey"] = modelKey
	}
	currentUsage = d.estimateForModel(currentUsage)
	if estimated := usageEstimatedCostFromData(currentUsage); estimated != nil {
		llmReturnUsage["estimatedCost"] = estimated
	}
	return currentUsage, true
}

func (d usageCostDecorator) estimateForModel(usage chat.UsageData) chat.UsageData {
	if d.models == nil {
		return usage
	}
	modelKey := strings.TrimSpace(usage.ModelKey)
	if modelKey == "" {
		return usage
	}
	if !usageHasBillableTokens(usage) {
		return usage
	}
	model, err := d.models.GetModel(modelKey)
	if err != nil || !modelPricingEnabled(model.Pricing) {
		return usage
	}
	return estimateUsageCost(usage, model.Pricing, d.billing)
}

func usageModelKeyFromEvent(data *stream.EventData, usage map[string]any) string {
	if data == nil || data.Payload == nil {
		return ""
	}
	usageRoot, _ := data.Payload["usage"].(map[string]any)
	modelNode, _ := data.Payload["model"].(map[string]any)
	contextWindow, _ := data.Payload["contextWindow"].(map[string]any)
	return strings.TrimSpace(contracts.FirstNonEmptyString(
		usage["modelKey"],
		usageRoot["modelKey"],
		contextWindow["modelKey"],
		modelNode["key"],
	))
}

func usageModelKeyFromDebugData(inner map[string]any, usage map[string]any) string {
	if inner == nil {
		return ""
	}
	modelNode, _ := inner["model"].(map[string]any)
	contextWindow, _ := inner["contextWindow"].(map[string]any)
	return strings.TrimSpace(contracts.FirstNonEmptyString(
		usage["modelKey"],
		contextWindow["modelKey"],
		modelNode["key"],
	))
}

func usageHasBillableTokens(usage chat.UsageData) bool {
	return usage.PromptTokens > 0 ||
		usage.CompletionTokens > 0 ||
		usage.TotalTokens > 0 ||
		usage.CachedTokens > 0 ||
		usage.PromptCacheHitTokens > 0 ||
		usage.PromptCacheMissTokens > 0
}

func estimateUsageCost(usage chat.UsageData, pricing models.ModelPricing, billing config.BillingConfig) chat.UsageData {
	if !modelPricingEnabled(pricing) {
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
	inputHit := float64(cacheHitTokens) * pricing.InputCacheHit / 1_000_000
	inputMiss := float64(cacheMissTokens) * pricing.InputCacheMiss / 1_000_000
	output := float64(usage.CompletionTokens) * pricing.Output / 1_000_000
	usage.EstimatedCostCurrency = currency
	usage.EstimatedCostInputHit = inputHit
	usage.EstimatedCostInputMiss = inputMiss
	usage.EstimatedCostOutput = output
	usage.EstimatedCostTotal = inputHit + inputMiss + output
	return usage
}

func modelPricingEnabled(pricing models.ModelPricing) bool {
	return pricing.InputCacheHit > 0 || pricing.InputCacheMiss > 0 || pricing.Output > 0
}

func usageEstimatedCostFromData(usage chat.UsageData) map[string]any {
	if strings.TrimSpace(usage.EstimatedCostCurrency) == "" {
		return nil
	}
	return map[string]any{
		"currency":       strings.ToUpper(strings.TrimSpace(usage.EstimatedCostCurrency)),
		"inputCacheHit":  usage.EstimatedCostInputHit,
		"inputCacheMiss": usage.EstimatedCostInputMiss,
		"output":         usage.EstimatedCostOutput,
		"total":          usage.EstimatedCostTotal,
	}
}
