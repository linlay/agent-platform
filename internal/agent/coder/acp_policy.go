package coder

import (
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

var defaultReasoningEfforts = []api.ReasoningEffortOption{
	{Key: "NONE", Label: "NONE"},
	{Key: "LOW", Label: "LOW"},
	{Key: "MEDIUM", Label: "MEDIUM"},
	{Key: "HIGH", Label: "HIGH"},
}

var reasoningEffortOrder = []string{"NONE", "LOW", "MEDIUM", "HIGH", "XHIGH", "MAX"}

func DefaultReasoningEffortOptions() []api.ReasoningEffortOption {
	return append([]api.ReasoningEffortOption(nil), defaultReasoningEfforts...)
}

func ModelConfigFromOptions(options api.CoderModelOptionsResponse) map[string]any {
	modelKey := strings.TrimSpace(options.DefaultModelKey)
	if modelKey == "" && len(options.Models) > 0 {
		modelKey = strings.TrimSpace(options.Models[0].Key)
	}
	if modelKey == "" {
		return nil
	}
	modelConfig := map[string]any{"modelKey": modelKey}
	reasoningEffort := strings.TrimSpace(options.DefaultReasoningEffort)
	if reasoningEffort != "" {
		reasoning := map[string]any{}
		if strings.EqualFold(reasoningEffort, "NONE") {
			reasoning["enabled"] = false
		} else {
			reasoning["enabled"] = true
			reasoning["effort"] = reasoningEffort
		}
		modelConfig["reasoning"] = reasoning
	}
	if serviceTier := strings.TrimSpace(options.DefaultServiceTier); serviceTier != "" {
		modelConfig["serviceTier"] = serviceTier
	}
	return modelConfig
}

func ModelConfigReasoningEffort(modelConfig map[string]any) string {
	reasoning := contracts.AnyMapNode(modelConfig["reasoning"])
	if enabled, ok := reasoning["enabled"].(bool); ok && !enabled {
		return "NONE"
	}
	return strings.TrimSpace(contracts.AnyStringNode(reasoning["effort"]))
}

func ModelOptionsFilterMode(agentKey string, mode string, acpProxyID string) string {
	if strings.TrimSpace(agentKey) == "" {
		return "native-only"
	}
	if IsACPBackend(mode, acpProxyID) {
		return "acp-only"
	}
	if IsMode(mode) {
		return "native-only"
	}
	return ""
}

func DefaultModelOptionKey(options []api.CoderModelOption, preferredKey string, defaultKey string) string {
	if len(options) == 0 {
		return ""
	}
	visible := make(map[string]bool, len(options))
	normalFallback := ""
	acpFallback := ""
	for _, option := range options {
		key := strings.TrimSpace(option.Key)
		if key == "" {
			continue
		}
		visible[key] = true
		if models.IsACPPassthroughProtocol(option.Protocol) {
			if acpFallback == "" {
				acpFallback = key
			}
			continue
		}
		if normalFallback == "" {
			normalFallback = key
		}
	}
	if key := strings.TrimSpace(preferredKey); visible[key] {
		return key
	}
	if key := strings.TrimSpace(defaultKey); visible[key] {
		return key
	}
	if normalFallback != "" {
		return normalFallback
	}
	return acpFallback
}

func DefaultServiceTier(isACPBackend bool, configuredServiceTier string, options []api.ServiceTierOption) string {
	if !isACPBackend {
		return ""
	}
	serviceTier, ok := NormalizeServiceTier(configuredServiceTier)
	if !ok || serviceTier == "" {
		return ""
	}
	if !ServiceTierInOptions(serviceTier, options) {
		return ""
	}
	return serviceTier
}

func ServiceTierOptions(isACPBackend bool, modelOptions []api.CoderModelOption) []api.ServiceTierOption {
	options := []api.ServiceTierOption{{Key: "STANDARD", Label: "Standard"}}
	if !isACPBackend {
		return options
	}
	seen := map[string]struct{}{"STANDARD": {}}
	extra := make([]string, 0, 4)
	for _, model := range modelOptions {
		for _, rawTier := range model.ServiceTiers {
			tier, ok := NormalizeServiceTier(rawTier)
			if !ok || tier == "" {
				continue
			}
			if _, exists := seen[tier]; exists {
				continue
			}
			seen[tier] = struct{}{}
			extra = append(extra, tier)
		}
	}
	sort.Strings(extra)
	for _, tier := range extra {
		options = append(options, api.ServiceTierOption{
			Key:   tier,
			Label: ServiceTierLabel(tier),
		})
	}
	return options
}

func ReasoningEffortOptions(isACPBackend bool, modelOptions []api.CoderModelOption) []api.ReasoningEffortOption {
	if !isACPBackend {
		return DefaultReasoningEffortOptions()
	}
	seen := map[string]struct{}{"NONE": {}}
	hasRuntimeEffort := false
	for _, model := range modelOptions {
		for _, rawEffort := range model.ReasoningEfforts {
			effort, ok := NormalizeReasoningEffort(rawEffort)
			if !ok || effort == "" || effort == "NONE" {
				continue
			}
			seen[effort] = struct{}{}
			hasRuntimeEffort = true
		}
	}
	if !hasRuntimeEffort {
		return DefaultReasoningEffortOptions()
	}
	options := make([]api.ReasoningEffortOption, 0, len(seen))
	for _, effort := range reasoningEffortOrder {
		if _, ok := seen[effort]; !ok {
			continue
		}
		options = append(options, api.ReasoningEffortOption{Key: effort, Label: effort})
	}
	return options
}

func ServiceTierInOptions(serviceTier string, options []api.ServiceTierOption) bool {
	serviceTier = strings.TrimSpace(serviceTier)
	if serviceTier == "" {
		return true
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Key), serviceTier) {
			return true
		}
	}
	return false
}

func ServiceTierLabel(serviceTier string) string {
	switch strings.ToUpper(strings.TrimSpace(serviceTier)) {
	case "STANDARD":
		return "Standard"
	case "FAST":
		return "Fast"
	case "FLEX":
		return "Flex"
	default:
		return strings.TrimSpace(serviceTier)
	}
}

func NormalizeReasoningEffort(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", true
	case "NONE":
		return "NONE", true
	case "LOW":
		return "LOW", true
	case "MEDIUM":
		return "MEDIUM", true
	case "HIGH":
		return "HIGH", true
	case "XHIGH", "EXTRA_HIGH":
		return "XHIGH", true
	case "MAX":
		return "MAX", true
	default:
		return "", false
	}
}

func NormalizeServiceTier(value string) (string, bool) {
	text := strings.ToUpper(strings.TrimSpace(value))
	switch text {
	case "", "STANDARD", "DEFAULT", "AUTO":
		return "", true
	default:
		return text, text != ""
	}
}

func ModelKeyInOptions(modelKey string, options []api.CoderModelOption) bool {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Key), modelKey) {
			return true
		}
	}
	return false
}

func ServiceTierAllowedForACPModel(serviceTier string, modelKey string, options []api.CoderModelOption) bool {
	serviceTier = strings.TrimSpace(serviceTier)
	if serviceTier == "" {
		return true
	}
	if strings.TrimSpace(modelKey) == "" {
		for _, option := range options {
			if serviceTierSupportedByACPModel(serviceTier, option) {
				return true
			}
		}
		return false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Key), modelKey) {
			return serviceTierSupportedByACPModel(serviceTier, option)
		}
	}
	return false
}

func serviceTierSupportedByACPModel(serviceTier string, option api.CoderModelOption) bool {
	for _, rawTier := range option.ServiceTiers {
		normalizedTier, ok := NormalizeServiceTier(rawTier)
		if !ok || normalizedTier == "" {
			continue
		}
		if strings.EqualFold(normalizedTier, serviceTier) {
			return true
		}
	}
	return false
}

func ReasoningEffortAllowedForACPModel(reasoningEffort string, modelKey string, options []api.CoderModelOption) bool {
	reasoningEffort, ok := NormalizeReasoningEffort(reasoningEffort)
	if !ok || reasoningEffort == "" || reasoningEffort == "NONE" {
		return ok
	}
	hasDeclaredEfforts := false
	for _, option := range options {
		if strings.TrimSpace(modelKey) != "" && !strings.EqualFold(strings.TrimSpace(option.Key), modelKey) {
			continue
		}
		supported := normalizedACPModelReasoningEfforts(option)
		if len(supported) == 0 {
			continue
		}
		hasDeclaredEfforts = true
		if supported[reasoningEffort] {
			return true
		}
	}
	if hasDeclaredEfforts {
		return false
	}
	switch reasoningEffort {
	case "LOW", "MEDIUM", "HIGH":
		return true
	default:
		return false
	}
}

func normalizedACPModelReasoningEfforts(option api.CoderModelOption) map[string]bool {
	out := map[string]bool{}
	for _, rawEffort := range option.ReasoningEfforts {
		effort, ok := NormalizeReasoningEffort(rawEffort)
		if ok && effort != "" {
			out[effort] = true
		}
	}
	return out
}
