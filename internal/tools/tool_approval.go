package tools

import (
	"agent-platform/internal/connector"
	. "agent-platform/internal/contracts"
	"context"
	"fmt"
)

func (r *ToolRouter) PrepareToolApproval(ctx context.Context, tool string, args map[string]any, e *ExecutionContext) (*ToolApproval, error) {
	planner, ok := r.namedHandler(tool).(ToolApprovalPlanner)
	if !ok {
		planner, ok = r.runtime.(ToolApprovalPlanner)
		if !ok {
			return nil, nil
		}
	}
	if owner, ok := connector.NativeToolConnector(tool); ok {
		if e == nil || !e.Session.AllowsTool(tool) || e.Session.NativeConnectorTools[tool] != owner || e.Session.ConnectorDirs[owner] == "" {
			return nil, fmt.Errorf("connector_not_mounted")
		}
	}
	plan, err := planner.PrepareToolApproval(ctx, tool, args, e)
	if err != nil || plan == nil {
		return plan, err
	}
	def, exists := r.Tool(tool)
	if !exists {
		return nil, fmt.Errorf("tool definition unavailable: %s", tool)
	}
	// Presentation is exclusively selected from tool configuration, never the handler.
	selected, err := selectConfirmationRule(def.Meta["confirmationRules"], args)
	if err != nil {
		return nil, err
	}
	resolved := *plan
	resolved.View = nil
	if selected != nil {
		resolved.View = selected.view
	}
	return &resolved, nil
}
