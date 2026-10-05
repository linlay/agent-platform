package runexec

import (
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
	"agent-platform/internal/stream"
)

type ProxyUsageTracker struct {
	decorator      UsageCostDecorator
	chatUsage      chat.UsageData
	runUsage       *chat.UsageData
	runModelKey    string
	runModelMixed  bool
	sawCurrentCost bool
}

func NewProxyUsageTracker(chatUsage chat.UsageData, runUsage *chat.UsageData, models *models.ModelRegistry, billing config.BillingConfig) *ProxyUsageTracker {
	return &ProxyUsageTracker{
		decorator: UsageCostDecorator{Models: models, Billing: billing},
		chatUsage: chatUsage,
		runUsage:  runUsage,
	}
}

func (t *ProxyUsageTracker) Decorate(event *stream.EventData) {
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

func (t *ProxyUsageTracker) decorateUsageSnapshot(event *stream.EventData) {
	if t.runUsage == nil {
		return
	}
	usage, _ := event.Payload["usage"].(map[string]any)
	if usage == nil {
		return
	}
	currentUsage, hasCurrent := t.decorator.DecorateCurrentUsage(event)
	if modelKey := strings.TrimSpace(currentUsage.ModelKey); modelKey != "" {
		t.recordRunModelKey(modelKey)
	}
	currentHasCost := UsageEstimatedCostFromData(currentUsage) != nil
	if run, _ := usage["run"].(map[string]any); run != nil {
		if currentHasCost {
			AddEstimatedUsageCost(t.runUsage, currentUsage)
			t.sawCurrentCost = true
		}
		MergeUsageMapIntoRunData(t.runUsage, run)
	} else if hasCurrent {
		*t.runUsage = AddUsageData(*t.runUsage, currentUsage)
		if currentHasCost {
			t.sawCurrentCost = true
		}
	}
	t.writeCumulativeUsage(usage)
}

func (t *ProxyUsageTracker) decorateTerminalUsage(event *stream.EventData) {
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
			terminalUsage := UsageDataFromMap(target)
			if modelKey := UsageModelKeyFromEvent(event, target); modelKey != "" {
				terminalUsage.ModelKey = modelKey
				target["modelKey"] = modelKey
				t.recordRunModelKey(modelKey)
			}
			terminalUsage = t.decorator.EstimateForModel(terminalUsage)
			if estimated := UsageEstimatedCostFromData(terminalUsage); estimated != nil {
				target["estimatedCost"] = estimated
			}
		}
		if modelKey := UsageModelKeyFromEvent(event, target); modelKey != "" {
			t.recordRunModelKey(modelKey)
		}
		MergeUsageMapIntoRunData(t.runUsage, target)
	}
	if !UsageHasData(*t.runUsage) {
		return
	}
	t.applyRunModelKey()
	runUsage := *t.runUsage
	runUsage.ModelKey = ""
	chatUsage := AddUsageData(t.chatUsage, *t.runUsage)
	chatUsage.ModelKey = ""
	event.Payload["usage"] = map[string]any{
		"chat": UsageDataMap(chatUsage),
		"run":  UsageDataMap(runUsage),
	}
}

func (t *ProxyUsageTracker) writeCumulativeUsage(usage map[string]any) {
	if t.runUsage == nil || usage == nil || !UsageHasData(*t.runUsage) {
		return
	}
	t.applyRunModelKey()
	runUsage := *t.runUsage
	runUsage.ModelKey = ""
	usage["run"] = UsageDataMapForSnapshot(runUsage)
	chatUsage := AddUsageData(t.chatUsage, *t.runUsage)
	chatUsage.ModelKey = ""
	usage["chat"] = UsageDataMapForSnapshot(chatUsage)
}

func (t *ProxyUsageTracker) recordRunModelKey(modelKey string) {
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

func (t *ProxyUsageTracker) applyRunModelKey() {
	if t == nil || t.runUsage == nil {
		return
	}
	if t.runModelMixed {
		t.runUsage.ModelKey = ""
		return
	}
	t.runUsage.ModelKey = strings.TrimSpace(t.runModelKey)
}
