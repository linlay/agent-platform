package hitl

import (
	"strings"

	"agent-platform/internal/view"
)

type SubcommandRule struct {
	Mode         string          `yaml:"mode"`
	View         *view.Reference `yaml:"view"`
	Match        string          `yaml:"match"`
	Level        int             `yaml:"level"`
	Title        string          `yaml:"title"`
	ViewportType string          `yaml:"viewportType"`
	ViewportKey  string          `yaml:"viewportKey"`
	Timeout      int             `yaml:"timeout"`
}

type CommandBlock struct {
	Command          string           `yaml:"command"`
	PassThroughFlags []string         `yaml:"passThroughFlags"`
	Subcommands      []SubcommandRule `yaml:"subcommands"`
}

type RuleFile struct {
	Key      string         `yaml:"key"`
	Enabled  *bool          `yaml:"enabled"`
	Commands []CommandBlock `yaml:"commands"`
}

type FlatRule struct {
	Mode             string
	View             *view.Reference
	RuleKey          string
	FileKey          string
	SourcePath       string
	Order            int
	Command          string
	Match            string
	MatchTokens      []string
	PassThroughFlags []string
	Level            int
	Title            string
	ViewportType     string
	ViewportKey      string
	Timeout          int
}

// Legacy YAML infers form from html only at the compatibility boundary.
// New definitions declare their interaction mode independently of rendering.
func (r FlatRule) EffectiveMode() string {
	if r.Mode != "" {
		return r.Mode
	}
	if strings.EqualFold(r.ViewportType, "html") {
		return "form"
	}
	return "approval"
}

func (r FlatRule) IsBuiltinApproval() bool {
	return r.EffectiveMode() == "approval" && r.View == nil && (r.ViewportType == "" || strings.EqualFold(r.ViewportType, "builtin"))
}

type CommandComponents struct {
	BaseCommand string
	Tokens      []string
}

type InterceptResult struct {
	Intercepted     bool
	Rule            FlatRule
	ParsedCommand   CommandComponents
	OriginalCommand string
	MatchedCommand  string
	MatchedWhole    bool
}
