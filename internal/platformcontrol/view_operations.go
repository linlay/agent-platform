package platformcontrol

import (
	"context"
	"fmt"

	"agent-platform/internal/toolinput"
	"agent-platform/internal/viewquery"
)

// ViewQueryOperations exposes read-only list actions to display views. They
// reuse the tools' argument validation and read logic, but not tool admission:
// a view query is the viewer's request, so no Agent mount, root Run, stage or
// approval applies. Results match the corresponding tool result shape.
func (h *ToolHandler) ViewQueryOperations() map[string]viewquery.Operation {
	return map[string]viewquery.Operation{
		"automation.list": func(_ context.Context, _ viewquery.Context, params map[string]any) (any, error) {
			if err := h.validateViewParams("automation_query", "list", params); err != nil {
				return nil, err
			}
			return h.invokeAutomation("automation_query", "list", params, nil)
		},
		"catalog.list": func(ctx context.Context, _ viewquery.Context, params map[string]any) (any, error) {
			if err := h.validateViewParams("catalog_query", "list", params); err != nil {
				return nil, err
			}
			// Views list Agents and connectors only; other catalog resources
			// stay with the management tools.
			if resourceType := stringValue(params, "resourceType"); resourceType != "agent" && resourceType != "connector" {
				return nil, fmt.Errorf("resourceType must be agent or connector")
			}
			return h.catalogQuery(ctx, "list", params)
		},
	}
}

func (h *ToolHandler) validateViewParams(tool, action string, params map[string]any) error {
	if err := validateControlEnums(tool, action, params); err != nil {
		return err
	}
	fields := argumentFields[tool+"."+action]
	toolinput.NormalizeBooleanFields(params, fields)
	return toolinput.Validate(params, fields, "params.")
}
