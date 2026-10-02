package contracts

import "context"

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

// NativeWait belongs to one invocation; the enclosing RunControl serializes
// skip/finish so a late skip can never wake a subsequent wait.
type NativeWait struct {
	Done     chan struct{}
	Resolved bool
	Reason   string
}

func (c *RunControl) RegisterNativeWait(toolID string) *NativeWait {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waits == nil {
		c.waits = map[string]*NativeWait{}
	}
	if w := c.waits[toolID]; w != nil {
		return w
	}
	w := &NativeWait{Done: make(chan struct{})}
	c.waits[toolID] = w
	return w
}
func (c *RunControl) FinishNativeWait(toolID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.waits[toolID]; w != nil {
		w.Resolved = true
	}
}
func (c *RunControl) SkipNativeWait(toolID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.waits[toolID]
	if w == nil {
		return "not_found"
	}
	if w.Resolved {
		return "already_resolved"
	}
	select {
	case <-w.Done:
		return "already_resolved"
	default:
		w.Resolved = true
		w.Reason = "skipped"
		close(w.Done)
	}
	return "accepted"
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

func (c *RunControl) ResolveNativeWait(toolID, reason string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.waits[toolID]
	if w == nil {
		return reason
	}
	if w.Reason != "" && reason != "canceled" {
		return w.Reason
	}
	w.Resolved = true
	w.Reason = reason
	return reason
}

// RunSteerPreparer binds attachment validation while a recovered Run is waiting
// without opening a model stream.
type RunSteerPreparer interface {
	BindSteerPreparer(QuerySession, *RunControl) error
}
