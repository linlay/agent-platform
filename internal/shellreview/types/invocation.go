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
	Raw        string
	// Expansion marks source that cannot be masked during connector review.
	// Literal separately records whether quote removal alone yields this word.
	Expansion bool
	Literal   bool
}

// WordOrigin records how the current argv value relates to its source word.
type WordOrigin string

const (
	WordOriginUnknown WordOrigin = ""
	WordOriginSyntax  WordOrigin = "syntax"
	WordOriginSplit   WordOrigin = "env_split"
)

// SourceWord follows a value through wrapper removal. Arg is its index in the
// original invocation's argv; the embedded span still points into that source.
// Generated values keep their source span but can never be marked Literal.
type SourceWord struct {
	WordSpan
	Arg    int
	Origin WordOrigin
	// ValueStart/End locate the extracted option value in the original source.
	ValueStart, ValueEnd int
}

// SourceWords never recovers literal provenance from expanded strings. Missing
// or misaligned spans remain unknown, including synthetic parser invocations.
func SourceWords(inv Invocation) []SourceWord {
	if len(inv.Argv) == 0 {
		return nil
	}
	out := make([]SourceWord, len(inv.Argv))
	for i := range out {
		out[i].Arg = -1
		if len(inv.Words) != len(inv.Argv) {
			continue
		}
		span := inv.Words[i]
		if span.Start < 0 || span.End <= span.Start {
			continue
		}
		out[i] = SourceWord{WordSpan: span, Arg: i, Origin: WordOriginSyntax}
	}
	return out
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
