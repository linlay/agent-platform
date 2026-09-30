package tools

import (
	"fmt"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

// ReviewImageAccess uses the same source parser and canonical path policy as
// image loading, without reading image bytes or consuming any approval.
func (t *RuntimeToolExecutor) ReviewImageAccess(toolName string, args map[string]any, ctx *ExecutionContext) ([]filetools.AccessPlan, error) {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if toolName != "image_generate" && toolName != "vision_recognize" {
		return nil, nil
	}
	items, ok := args["images"].([]any)
	if !ok || len(items) == 0 {
		return nil, nil // The executor owns argument/model validation errors.
	}
	items = append([]any(nil), items...)
	imageCount := len(items)
	if toolName == "image_generate" && args["mask"] != nil {
		items = append(items, args["mask"])
	}
	policy := visionImageSourcePolicy()
	if toolName == "image_generate" {
		policy = imageGenerateSourcePolicy()
	}
	plans := make([]filetools.AccessPlan, 0, len(items))
	for i, raw := range items {
		if toolName == "image_generate" {
			normalized, result, handled := normalizeImageGenerateSource(raw, "image source", i, i == imageCount)
			if handled {
				return nil, fmt.Errorf("%s", result.Error)
			}
			raw = normalized
		}
		_, plan, result, handled := t.planToolImageSource(raw, ctx, policy)
		if handled {
			return nil, fmt.Errorf("%s", result.Error)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (r *ToolRouter) ReviewImageAccess(toolName string, args map[string]any, ctx *ExecutionContext) ([]filetools.AccessPlan, error) {
	if reviewer, ok := r.runtime.(interface {
		ReviewImageAccess(string, map[string]any, *ExecutionContext) ([]filetools.AccessPlan, error)
	}); ok {
		return reviewer.ReviewImageAccess(toolName, args, ctx)
	}
	return nil, nil
}
