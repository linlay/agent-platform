package types

type FileAccess struct {
	Path      string
	Write     bool
	Recursive bool
}

// GitCheck names the repository state an analyzed Git invocation depends on.
// The caller must verify it before treating the invocation as non-executing.
type GitCheck string

const (
	GitCheckNone GitCheck = ""
	// GitCheckRead requires that effective configuration defines no program
	// that read commands run implicitly (fsmonitor, diff drivers, filters, gpg).
	GitCheckRead GitCheck = "read"
	// GitCheckWrite additionally requires that no repository hook is active.
	GitCheckWrite GitCheck = "write"
	// GitCheckNetwork additionally requires standard credential helpers and no
	// custom SSH or askpass programs.
	GitCheckNetwork GitCheck = "network"
)

type Effects struct {
	Files []FileAccess
	// Incomplete marks options or grammar the analyzer does not model. Visible
	// file operands are then treated as writes; it does not imply code execution.
	Incomplete bool
	// ExecutesCode means the invocation can run code, or take behavior from a
	// source this analysis does not read (such as a config file), that operands
	// do not describe.
	ExecutesCode   bool
	RemoteMutation bool
	// Destructive marks recursive deletion and discarding uncommitted work.
	Destructive bool
	// RepoWrite marks Git commands that mutate repository metadata in cwd.
	RepoWrite bool
	GitCheck  GitCheck
	// SSHAgent marks network operations that may authenticate with an SSH agent.
	SSHAgent bool
}
