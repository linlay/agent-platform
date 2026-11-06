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
	// Inline marks a system interpreter running program text written literally
	// in this command; InlineArgs are its argv indices (none for stdin text).
	Inline      bool
	InlineArgs  []int
	BlockReason string
	// Env is the environment visible to Argv: the reviewed environment plus
	// shell scope, inline assignments and env(1) assignments.
	Env              map[string]string
	StartupDeps      []Dep // captured before each native wrapper starts
	DependencySHA256 string
	DependencyReason string
}

// BashEnvironment inspects the actual execution filesystem. A missing/failed
// container resolver is opaque; it must never fall back to the host PATH.
type BashEnvironment struct {
	// SystemPrograms optionally supplies a frozen Host installation snapshot.
	// Nil uses the process-start snapshot; container identity ignores this map.
	// The trusted caller owns the snapshot and must not mutate it during review.
	SystemPrograms map[string]string
	// InspectNullDevice uses an image-verified immutable guest stat entry and
	// returns numeric mode|major|minor; absent or failed probes stay unresolved.
	InspectNullDevice func() (string, error)
	InspectConfig     func(path string) (FileInspection, error)
	AccountHome       func() (string, error)
	// Launch describes the actual transport. Unattested guest shells are unknown;
	// only a verified profile may declare SyntaxSupported.
	Launch    *LaunchBinding
	Resolve   func(name, cwd string, env map[string]string) (string, error)
	Canonical func(path, cwd string) (string, error)
	Directory func(path string) (string, error)
	// PathKind inspects the guest filesystem only; Unknown never consults Host.
	PathKind func(path string) (PathKind, error)
	Inspect  func(path string) (header, sha256 string, err error)
}

var ErrBashTemporaryEscape = errors.New("container temporary path escapes /tmp through a symlink")

// PathKind distinguishes transfer destination forms, not device identities.
type PathKind string

const (
	PathUnknown      PathKind = ""
	PathDirectory    PathKind = "directory"
	PathNonDirectory PathKind = "file"
)
