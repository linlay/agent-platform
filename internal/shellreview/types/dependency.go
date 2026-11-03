package types

// ConfigCandidate describes lookup without performing IO in a command file.
// A nonempty StopWhenSet variable suppresses later candidates even if its file
// is absent (WGETRC). EmptyPath models an explicitly empty HOME when needed;
// NativeHome means the account home, not the run's HOME.
type ConfigCandidate struct {
	Variable    string
	Relative    string
	Path        string
	NativeHome  bool
	StopWhenSet bool
	EmptyPath   string
}
type FileInspection struct {
	Exists    bool
	Canonical string
	SHA256    string
	Alias     bool
}
