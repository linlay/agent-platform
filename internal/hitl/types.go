package hitl

type SubcommandRule struct {
	Match        string `yaml:"match"`
	Level        int    `yaml:"level"`
	HITLType     string `yaml:"hitlType"`
	ViewportType string `yaml:"viewportType"`
	ViewportKey  string `yaml:"viewportKey"`
}

type CommandBlock struct {
	Command     string           `yaml:"command"`
	Subcommands []SubcommandRule `yaml:"subcommands"`
}

type RuleFile struct {
	Key      string         `yaml:"key"`
	Enabled  *bool          `yaml:"enabled"`
	Commands []CommandBlock `yaml:"commands"`
}

type FlatRule struct {
	FileKey      string
	SourcePath   string
	Order        int
	Command      string
	Match        string
	MatchTokens  []string
	Level        int
	HITLType     string
	ViewportType string
	ViewportKey  string
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
}
