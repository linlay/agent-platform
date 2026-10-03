package contracts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ToolApproval is an exact, one-shot review prepared by the business handler.
// The LLM loop displays it without knowing its resource domain.
type ToolApproval struct {
	Fingerprint string         `json:"fingerprint"`
	Title       string         `json:"title"`
	ViewportKey string         `json:"viewportKey,omitempty"`
	Form        map[string]any `json:"form,omitempty"`
}
type ToolApprovalPlanner interface {
	PrepareToolApproval(context.Context, string, map[string]any, *ExecutionContext) (*ToolApproval, error)
}

func ToolApprovalFingerprint(e *ExecutionContext, tool, action, digest string) string {
	if e == nil {
		return ""
	}
	b, _ := json.Marshal([]string{e.Session.RunID, e.Session.Subject, e.Session.AgentKey, e.CurrentToolID, tool, action, digest})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ConsumeToolApproval(e *ExecutionContext, fingerprint string) bool {
	if e == nil || fingerprint == "" || !e.ToolApprovals[fingerprint] {
		return false
	}
	delete(e.ToolApprovals, fingerprint)
	return true
}
