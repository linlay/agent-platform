package contracts

import (
	"strings"
	"time"

	"agent-platform/internal/config"
)

type RetryPolicy struct {
	MaxCalls   int `json:"maxCalls,omitempty"`
	TimeoutMs  int `json:"timeoutMs,omitempty"`
	RetryCount int `json:"retryCount,omitempty"`
}

type HitlPolicy struct {
	TimeoutMs int            `json:"timeoutMs,omitempty"`
	Question  HitlModePolicy `json:"question,omitempty"`
	Approval  HitlModePolicy `json:"approval,omitempty"`
	Form      HitlModePolicy `json:"form,omitempty"`
	Plan      HitlModePolicy `json:"plan,omitempty"`
}

type HitlModePolicy struct {
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

type StageBudget struct {
	MaxSteps int         `json:"maxSteps,omitempty"`
	Tool     RetryPolicy `json:"tool,omitempty"`
}

type Budget struct {
	RunTimeoutMs int                    `json:"runTimeoutMs,omitempty"`
	MaxSteps     int                    `json:"maxSteps,omitempty"`
	Model        RetryPolicy            `json:"model,omitempty"`
	Tool         RetryPolicy            `json:"tool,omitempty"`
	Hitl         HitlPolicy             `json:"hitl,omitempty"`
	Stages       map[string]StageBudget `json:"stages,omitempty"`
}

func DefaultBudget(cfg config.Config) Budget {
	return Budget{
		RunTimeoutMs: cfg.Defaults.Budget.RunTimeoutMs,
		MaxSteps:     cfg.Defaults.Budget.MaxSteps,
		Model: RetryPolicy{
			MaxCalls:   cfg.Defaults.Budget.Model.MaxCalls,
			TimeoutMs:  cfg.Defaults.Budget.Model.TimeoutMs,
			RetryCount: cfg.Defaults.Budget.Model.RetryCount,
		},
		Tool: RetryPolicy{
			MaxCalls:   cfg.Defaults.Budget.Tool.MaxCalls,
			TimeoutMs:  cfg.Defaults.Budget.Tool.TimeoutMs,
			RetryCount: cfg.Defaults.Budget.Tool.RetryCount,
		},
		Hitl: HitlPolicy{
			TimeoutMs: cfg.Defaults.Budget.Hitl.TimeoutMs,
			Question:  hitlModePolicyFromConfig(cfg.Defaults.Budget.Hitl.Question),
			Approval:  hitlModePolicyFromConfig(cfg.Defaults.Budget.Hitl.Approval),
			Form:      hitlModePolicyFromConfig(cfg.Defaults.Budget.Hitl.Form),
			Plan:      hitlModePolicyFromConfig(cfg.Defaults.Budget.Hitl.Plan),
		},
	}
}

func ResolveBudget(cfg config.Config, overrides map[string]any) Budget {
	budget := NormalizeBudget(DefaultBudget(cfg))
	if len(overrides) == 0 {
		return budget
	}
	rootStepsOverridden := false
	rootToolExplicit := false
	if value := anyIntNode(overrides["runTimeoutMs"]); value > 0 {
		budget.RunTimeoutMs = value
	}
	if value := anyIntNode(overrides["maxSteps"]); value > 0 {
		budget.MaxSteps = value
		rootStepsOverridden = true
	}
	if model := anyMapNode(overrides["model"]); len(model) > 0 {
		if value := anyIntNode(model["maxCalls"]); value > 0 && !rootStepsOverridden {
			budget.MaxSteps = value
			rootStepsOverridden = true
		}
		budget.Model = mergeRetryPolicy(budget.Model, model)
	}
	if tool := anyMapNode(overrides["tool"]); len(tool) > 0 {
		if anyIntNode(tool["maxCalls"]) > 0 {
			rootToolExplicit = true
		}
		budget.Tool = mergeRetryPolicy(budget.Tool, tool)
	}
	if hitl := anyMapNode(overrides["hitl"]); len(hitl) > 0 {
		budget.Hitl = mergeHitlPolicy(budget.Hitl, hitl)
	}
	if stages := anyMapNode(overrides["stages"]); len(stages) > 0 {
		budget.Stages = mergeStageBudgets(budget.Stages, stages)
	}
	if rootStepsOverridden && !rootToolExplicit && budget.MaxSteps > 0 {
		budget.Tool.MaxCalls = budget.MaxSteps * 2
	}
	return NormalizeBudget(budget)
}

func normalizeBudget(b Budget) Budget {
	hadStepOverride := b.MaxSteps > 0 || b.Model.MaxCalls > 0
	if b.RunTimeoutMs <= 0 {
		b.RunTimeoutMs = 300000
	}
	if b.MaxSteps <= 0 {
		b.MaxSteps = b.Model.MaxCalls
	}
	if b.MaxSteps <= 0 {
		b.MaxSteps = 100
	}
	b.Model = normalizeRetryPolicy(b.Model, RetryPolicy{MaxCalls: b.MaxSteps, TimeoutMs: 120000, RetryCount: 0})
	b.Model.MaxCalls = b.MaxSteps
	toolFallbackMaxCalls := 60
	if hadStepOverride {
		toolFallbackMaxCalls = b.MaxSteps * 2
	}
	b.Tool = normalizeRetryPolicy(b.Tool, RetryPolicy{MaxCalls: toolFallbackMaxCalls, TimeoutMs: 300000, RetryCount: 0})
	if b.Stages != nil {
		normalizedStages := map[string]StageBudget{}
		for key, stage := range b.Stages {
			stage = normalizeStageBudget(stage)
			if stage.MaxSteps > 0 || stage.Tool.MaxCalls > 0 || stage.Tool.TimeoutMs > 0 || stage.Tool.RetryCount > 0 {
				normalizedStages[normalizeStageBudgetKey(key)] = stage
			}
		}
		b.Stages = normalizedStages
		if len(normalizedStages) == 0 {
			b.Stages = nil
		}
	}
	return b
}

func hitlModePolicyFromConfig(cfg config.HitlModeBudgetConfig) HitlModePolicy {
	return HitlModePolicy{TimeoutMs: cfg.TimeoutMs}
}

func normalizeStageBudget(stage StageBudget) StageBudget {
	if stage.MaxSteps > 0 && stage.Tool.MaxCalls <= 0 {
		stage.Tool.MaxCalls = stage.MaxSteps * 2
	}
	if stage.Tool.RetryCount < 0 {
		stage.Tool.RetryCount = 0
	}
	return stage
}

func normalizeRetryPolicy(policy RetryPolicy, fallback RetryPolicy) RetryPolicy {
	if policy.MaxCalls <= 0 {
		policy.MaxCalls = fallback.MaxCalls
	}
	if policy.TimeoutMs <= 0 {
		policy.TimeoutMs = fallback.TimeoutMs
	}
	if policy.RetryCount < 0 {
		policy.RetryCount = 0
	}
	return policy
}

func mergeRetryPolicy(base RetryPolicy, overrides map[string]any) RetryPolicy {
	policy := base
	if value := anyIntNode(overrides["maxCalls"]); value > 0 {
		policy.MaxCalls = value
	}
	if value := anyIntNode(overrides["timeoutMs"]); value > 0 {
		policy.TimeoutMs = value
	}
	if value, ok := readOptionalInt(overrides["retryCount"]); ok {
		policy.RetryCount = maxInt(value, 0)
	}
	return policy
}

func mergeHitlPolicy(base HitlPolicy, overrides map[string]any) HitlPolicy {
	policy := base
	if value := anyIntNode(overrides["timeoutMs"]); value > 0 {
		policy.TimeoutMs = value
	}
	policy.Question = mergeHitlModePolicy(policy.Question, anyMapNode(overrides["question"]))
	policy.Approval = mergeHitlModePolicy(policy.Approval, anyMapNode(overrides["approval"]))
	policy.Form = mergeHitlModePolicy(policy.Form, anyMapNode(overrides["form"]))
	policy.Plan = mergeHitlModePolicy(policy.Plan, anyMapNode(overrides["plan"]))
	return policy
}

func mergeHitlModePolicy(base HitlModePolicy, overrides map[string]any) HitlModePolicy {
	policy := base
	if value := anyIntNode(overrides["timeoutMs"]); value > 0 {
		policy.TimeoutMs = value
	}
	return policy
}

const DefaultHITLTimeoutMs int64 = 600000

func ResolveHITLTimeout(mode string, itemTimeoutMs int64, budget Budget) int64 {
	mode = strings.ToLower(strings.TrimSpace(mode))
	hitl := budget.Hitl
	if itemTimeoutMs > 0 && (mode == "approval" || mode == "form" || mode == "question") {
		return itemTimeoutMs
	}
	if modeTimeout := hitlModeTimeout(hitl, mode); modeTimeout > 0 {
		return int64(modeTimeout)
	}
	if hitl.TimeoutMs > 0 {
		return int64(hitl.TimeoutMs)
	}
	return DefaultHITLTimeoutMs
}

func hitlModeTimeout(hitl HitlPolicy, mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		return hitl.Question.TimeoutMs
	case "approval":
		return hitl.Approval.TimeoutMs
	case "form":
		return hitl.Form.TimeoutMs
	case "plan":
		return hitl.Plan.TimeoutMs
	default:
		return 0
	}
}

func mergeStageBudgets(base map[string]StageBudget, overrides map[string]any) map[string]StageBudget {
	out := cloneStageBudgets(base)
	if out == nil {
		out = map[string]StageBudget{}
	}
	for rawKey, rawValue := range overrides {
		key := normalizeStageBudgetKey(rawKey)
		if key == "" {
			continue
		}
		raw := anyMapNode(rawValue)
		if len(raw) == 0 {
			continue
		}
		stage := out[key]
		if value := anyIntNode(raw["maxSteps"]); value > 0 {
			stage.MaxSteps = value
		}
		if tool := anyMapNode(raw["tool"]); len(tool) > 0 {
			stage.Tool = mergeRetryPolicy(stage.Tool, tool)
		}
		out[key] = stage
	}
	return out
}

func cloneStageBudgets(values map[string]StageBudget) map[string]StageBudget {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]StageBudget, len(values))
	for key, value := range values {
		out[normalizeStageBudgetKey(key)] = value
	}
	return out
}

func normalizeStageBudgetKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func readOptionalInt(value any) (int, bool) {
	number := anyIntNode(value)
	switch value.(type) {
	case int, int64, float64, string:
		return number, true
	default:
		return 0, false
	}
}

func (b Budget) RunTimeout() time.Duration {
	return time.Duration(maxInt(b.RunTimeoutMs, 1)) * time.Millisecond
}

func (p RetryPolicy) Timeout() time.Duration {
	return time.Duration(maxInt(p.TimeoutMs, 1)) * time.Millisecond
}
