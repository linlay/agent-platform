package bashast

type ParseKind int

const (
	Simple ParseKind = iota
	TooComplex
	ParseUnavailable
)

type SimpleCommand struct {
	Argv      []string
	EnvVars   []EnvVar
	Redirects []Redirect
	Text      string
}

type EnvVar struct {
	Name  string
	Value string
}

type Redirect struct {
	Op     string
	Target string
	Fd     int
}

type ParseResult struct {
	Kind     ParseKind
	Commands []SimpleCommand
	Reason   string
	NodeType string
}

type EmbeddedScript struct {
	Language string
	Code     string
	ArgIndex int
}

const (
	CommandSubstitutionPlaceholder = "__CMDSUB_OUTPUT__"
	TrackedVariablePlaceholder     = "__TRACKED_VAR__"
)
