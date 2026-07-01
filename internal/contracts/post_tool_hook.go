package contracts

// PostToolHookResult controls what happens after a tool call.
type PostToolHookResult int

const (
	PostToolContinue PostToolHookResult = iota
	PostToolStop
)
