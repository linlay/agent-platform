package hitl

import (
	"agent-platform/internal/view"
)

// FlatRule describes why one tool call waits for a person: a built-in
// approval (access policy, security review) or a connector VIEW form.
type FlatRule struct {
	Mode    string
	View    *view.Reference
	RuleKey string
	Level   int
	Title   string
	Timeout int
}

// Builtin HTML reviews default to form; other rules default to approval.
// Connector forms must declare their interaction mode explicitly.
func (r FlatRule) EffectiveMode() string {
	if r.Mode != "" {
		return r.Mode
	}
	if r.View != nil && r.View.ConnectorID == "" && r.View.Renderer == "html" {
		return "form"
	}
	return "approval"
}

func (r FlatRule) IsBuiltinApproval() bool {
	return r.EffectiveMode() == "approval" && (r.View == nil || (r.View.ConnectorID == "" && r.View.Renderer == "native"))
}

type InterceptResult struct {
	// Conflict refuses the call outright with this reason.
	Conflict        string
	Intercepted     bool
	Rule            FlatRule
	OriginalCommand string
	MatchedCommand  string
	MatchedWhole    bool
	// Intercept binds a connector VIEW form to the command it reviews.
	Intercept *ViewIntercept
}

// ViewIntercept is a connector command reviewed in its VIEW form. Prefix is
// the entry as written; the command is rebuilt from it and the submitted
// params through the action template.
type ViewIntercept struct {
	ConnectorID string
	Prefix      []string
	Action      view.InterceptAction
}
