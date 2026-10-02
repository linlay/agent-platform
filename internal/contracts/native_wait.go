package contracts

import (
	"context"
	"strings"

	"agent-platform/internal/api"
)

// WaitCondition is deliberately a closed union. Providers must reject unknown
// targets rather than silently turning lookup failures into timeouts.
type WaitCondition struct {
	Type            string `json:"type"`
	RunID           string `json:"runId,omitempty"`
	FilePath        string `json:"filePath,omitempty"`
	After           int64  `json:"after,omitempty"`
	AgentKey        string `json:"agentKey,omitempty"`
	RefreshID       string `json:"refreshId,omitempty"`
	ConnectorID     string `json:"connectorId,omitempty"`
	AuthorizationID string `json:"authorizationId,omitempty"`
}
type WaitConditionState struct {
	Index     int           `json:"index"`
	Condition WaitCondition `json:"condition"`
	Satisfied bool          `json:"satisfied"`
	Status    string        `json:"status"`
}
type WaitConditionProvider interface {
	CheckWaitCondition(context.Context, WaitCondition, *ExecutionContext) (bool, string, error)
}

// QueuedSteersBlank reports whether every queued steer is blank, i.e. the user
// asked to continue without giving new input. It does not consume the queue.
func (c *RunControl) QueuedSteersBlank() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, steer := range c.steerQueue {
		if !blankSteer(steer) {
			return false
		}
	}
	return len(c.steerQueue) > 0
}

func blankSteer(req api.SteerRequest) bool {
	return strings.TrimSpace(req.Message) == "" && len(req.References) == 0
}

// WaitCheckpoint is private runtime state stored with an awaiting record. It
// contains no environment values, credentials, or one-shot approvals.
type WaitCheckpoint struct {
	StartedAt      int64  `json:"startedAt"`
	Budget         Budget `json:"budget"`
	BudgetPausedMs int64  `json:"budgetPausedMs"`
	WaitCount      int    `json:"waitCount"`
	WaitTotalMs    int64  `json:"waitTotalMs"`
	ModelCalls     int    `json:"modelCalls"`
	ToolCalls      int    `json:"toolCalls"`
	ToolRounds     int    `json:"toolRounds"`
	Unrecoverable  bool   `json:"unrecoverable"`
}

// RunSteerPreparer binds attachment validation while a recovered Run is waiting
// without opening a model stream.
type RunSteerPreparer interface {
	BindSteerPreparer(QuerySession, *RunControl) error
}
