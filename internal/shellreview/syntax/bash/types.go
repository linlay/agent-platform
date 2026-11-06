package bash

import "agent-platform/internal/shellreview/types"

type ParseKind int

const (
	Simple ParseKind = iota
	TooComplex
	ParseUnavailable
)

type ParseResult struct {
	Kind     ParseKind
	Commands []types.Invocation
	Reason   string
	NodeType string
}

// GlobExpander resolves a for-loop glob at review time. Relative patterns are
// expanded in every listed directory ("" is the review cwd); ok=false keeps the
// loop variable unknown. Without an expander no loop is walked per value.
type GlobExpander func(pattern string, dirs []string) (values []string, ok bool)

const (
	CommandSubstitutionPlaceholder = "__CMDSUB_OUTPUT__"
	TrackedVariablePlaceholder     = "__TRACKED_VAR__"
)
