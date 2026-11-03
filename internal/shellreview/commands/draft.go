package commands

import "agent-platform/internal/shellreview/types"

type effectDraft struct {
	Files    []types.FileAccess
	Deps     []types.Dep
	RepoPath string
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
	GitCheck  types.GitCheck
	// SSHAgent marks network operations that may authenticate with an SSH agent.
	SSHAgent bool
}
