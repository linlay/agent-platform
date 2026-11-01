package types

// Invocation describes a parsed call without assigning identity or permissions.
type Invocation struct {
	Words     []WordSpan        // source spans; shell substitutions stay visible in review projections
	Uncertain bool              // loop iterations may observe different variable values
	Variables map[string]string // statically known shell scope before this command
	Argv      []string
	EnvVars   []EnvVar
	Redirects []Redirect
	Text      string
}

type WordSpan struct {
	Start, End int
	// Expansion marks source that cannot be masked during connector review.
	// Literal separately records whether quote removal alone yields this word.
	Expansion bool
	Literal   bool
}

type EnvVar struct {
	Name  string
	Value string
}

type Redirect struct {
	Op               string
	Target           string
	TargetWord       WordSpan
	Fd               int
	IsHeredoc        bool
	HeredocBodyStart int
	HeredocBodyEnd   int
}
