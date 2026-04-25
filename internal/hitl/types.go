package hitl

type SubcommandRule struct {
	Match        string `yaml:"match"`
	Level        int    `yaml:"level"`
	Title        string `yaml:"title"`
	ViewportType string `yaml:"viewportType"`
	ViewportKey  string `yaml:"viewportKey"`
	TimeoutMs    int    `yaml:"timeoutMs"`
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
	TimeoutMs        int
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
