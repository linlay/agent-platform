package app

import (
	"agent-platform/internal/contracts"

	"agent-platform/internal/runops"
	"context"
	"fmt"
)

type authorizationWaitReader interface {
	AuthorizationWaitStatus(string, string) (string, error)
}
type waitEventProvider struct {
	runs  *runops.ToolHandler
	kbase interface {
		RefreshOperationStatus(string, string) (string, error)
	}
	auth authorizationWaitReader
}

func (p waitEventProvider) CheckWaitCondition(ctx context.Context, c contracts.WaitCondition, e *contracts.ExecutionContext) (bool, string, error) {
	if e == nil {
		return false, "", fmt.Errorf("wait requires an execution context")
	}
	switch c.Type {
	case "run.terminal":
		return p.runs.CheckWaitCondition(ctx, c, e)
	case "kbase.refreshTerminal":
		if c.AgentKey != e.Session.AgentKey {
			return false, "", fmt.Errorf("KBASE target must belong to the current Agent")
		}
		status, err := p.kbase.RefreshOperationStatus(c.AgentKey, c.RefreshID)
		return status != "running" && status != "pending", status, err
	case "connector.authorizationTerminal":
		if _, ok := e.Session.ConnectorDirs[c.ConnectorID]; !ok {
			return false, "", fmt.Errorf("connector is not mounted for the current Agent")
		}
		status, err := p.auth.AuthorizationWaitStatus(c.ConnectorID, c.AuthorizationID)
		return status == "authorized" || status == "failed" || status == "canceled" || status == "expired" || status == "interrupted", status, err
	}
	return false, "", fmt.Errorf("unsupported wait condition %q", c.Type)
}
