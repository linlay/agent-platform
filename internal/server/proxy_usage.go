package server

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

type proxyUsageTracker struct {
	decorator      usageCostDecorator
	chatUsage      chat.UsageData
	runUsage       *chat.UsageData
	runModelKey    string
	runModelMixed  bool
	sawCurrentCost bool
}

func newProxyUsageTracker(chatUsage chat.UsageData, runUsage *chat.UsageData, models *models.ModelRegistry, billing config.BillingConfig) *proxyUsageTracker {
	return &proxyUsageTracker{
		decorator: usageCostDecorator{models: models, billing: billing},
		chatUsage: chatUsage,
		runUsage:  runUsage,
	}
}

func (t *proxyUsageTracker) Decorate(event *stream.EventData) {
	if t == nil || event == nil {
		return
	}
	switch event.Type {
	case "usage.snapshot":
		t.decorateUsageSnapshot(event)
	case "run.complete", "run.error", "run.cancel":
		t.decorateTerminalUsage(event)
	}
}

func (t *proxyUsageTracker) decorateUsageSnapshot(event *stream.EventData) {
	if t.runUsage == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	currentUsage, hasCurrent := t.decorator.decorateCurrentUsage(event)
	if modelKey := strings.TrimSpace(currentUsage.ModelKey); modelKey != "" {
		t.recordRunModelKey(modelKey)
	}
	currentHasCost := usageEstimatedCostFromData(currentUsage) != nil
	if run, _ := usage["run"].(map[string]any); run != nil {
		if currentHasCost {
			addEstimatedUsageCost(t.runUsage, currentUsage)
			t.sawCurrentCost = true
		}
		mergeUsageMapIntoRunData(t.runUsage, run)
	} else if hasCurrent {
		*t.runUsage = addUsageData(*t.runUsage, currentUsage)
		if currentHasCost {
			t.sawCurrentCost = true
		}
	}
	t.writeCumulativeUsage(usage)
}

func (t *proxyUsageTracker) decorateTerminalUsage(event *stream.EventData) {
	if t.runUsage == nil || event == nil || event.Payload == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage != nil {
		target := usage
		if run, _ := usage["run"].(map[string]any); run != nil {
			target = run
		}
		if !t.sawCurrentCost && strings.TrimSpace(t.runUsage.EstimatedCostCurrency) == "" {
			terminalUsage := usageDataFromMap(target)
			if modelKey := usageModelKeyFromEvent(event, target); modelKey != "" {
				terminalUsage.ModelKey = modelKey
				target["modelKey"] = modelKey
				t.recordRunModelKey(modelKey)
			}
			terminalUsage = t.decorator.estimateForModel(terminalUsage)
			if estimated := usageEstimatedCostFromData(terminalUsage); estimated != nil {
				target["estimatedCost"] = estimated
			}
		}
		if modelKey := usageModelKeyFromEvent(event, target); modelKey != "" {
			t.recordRunModelKey(modelKey)
		}
		mergeUsageMapIntoRunData(t.runUsage, target)
	}
	if !usageHasData(*t.runUsage) {
		return
	}
	t.applyRunModelKey()
	runUsage := *t.runUsage
	runUsage.ModelKey = ""
	chatUsage := addUsageData(t.chatUsage, *t.runUsage)
	chatUsage.ModelKey = ""
	event.Payload["usage"] = map[string]any{
		"chat": usageDataMap(chatUsage),
		"run":  usageDataMap(runUsage),
	}
}

func (t *proxyUsageTracker) writeCumulativeUsage(usage map[string]any) {
	if t.runUsage == nil || usage == nil || !usageHasData(*t.runUsage) {
		return
	}
	t.applyRunModelKey()
	runUsage := *t.runUsage
	runUsage.ModelKey = ""
	usage["run"] = usageDataMapForSnapshot(runUsage)
	chatUsage := addUsageData(t.chatUsage, *t.runUsage)
	chatUsage.ModelKey = ""
	usage["chat"] = usageDataMapForSnapshot(chatUsage)
}

func (t *proxyUsageTracker) recordRunModelKey(modelKey string) {
	if t == nil || t.runModelMixed {
		return
	}
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return
	}
	if t.runModelKey == "" {
		t.runModelKey = modelKey
		return
	}
	if t.runModelKey != modelKey {
		t.runModelKey = ""
		t.runModelMixed = true
	}
}

func (t *proxyUsageTracker) applyRunModelKey() {
	if t == nil || t.runUsage == nil {
		return
	}
	if t.runModelMixed {
		t.runUsage.ModelKey = ""
		return
	}
	t.runUsage.ModelKey = strings.TrimSpace(t.runModelKey)
}
