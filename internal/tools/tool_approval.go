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
		return nil, nil
	}
	if owner, ok := connector.NativeToolConnector(tool); ok {
		if e == nil || !e.Session.AllowsTool(tool) || e.Session.NativeConnectorTools[tool] != owner || e.Session.ConnectorDirs[owner] == "" {
			return nil, fmt.Errorf("connector_not_mounted")
		}
	}
	return planner.PrepareToolApproval(ctx, tool, args, e)
}
