package types

import (
	"errors"
)

// BashExecution is analysis only: the original command is never rewritten.
type BashExecution struct {
	Connector          bool
	ConnectorID        string
	Argv               []string
	Words              []SourceWord // aligned with Argv after every trusted wrapper
	Cwd                string
	Program            string
	Script             string
	Identity           string
	CommandIdentity    CommandIdentity
	Opaque             bool
	Uncertain          bool
	Wrapped            bool
	TrustedInterpreter bool
	BlockReason        string
	// Env is the environment visible to Argv: the reviewed environment plus
	// shell scope, inline assignments and env(1) assignments.
	Env map[string]string
}

// BashEnvironment inspects the actual execution filesystem. A missing/failed
// container resolver is opaque; it must never fall back to the host PATH.
type BashEnvironment struct {
	// SystemPrograms optionally supplies a frozen Host installation snapshot.
	// Nil uses the process-start snapshot; container identity ignores this map.
	// The trusted caller owns the snapshot and must not mutate it during review.
	SystemPrograms map[string]string
	// Launch is supplied only for Host commands; containers use their own Bash.
	Launch    *LaunchBinding
	Resolve   func(name, cwd string, env map[string]string) (string, error)
	Canonical func(path, cwd string) (string, error)
	Directory func(path string) (string, error)
	Inspect   func(path string) (header, sha256 string, err error)
}

var ErrBashTemporaryEscape = errors.New("container temporary path escapes /tmp through a symlink")
