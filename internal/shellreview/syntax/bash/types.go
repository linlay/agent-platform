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

const (
	CommandSubstitutionPlaceholder = "__CMDSUB_OUTPUT__"
	TrackedVariablePlaceholder     = "__TRACKED_VAR__"
)
