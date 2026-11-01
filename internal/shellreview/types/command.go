package types

import "agent-platform/internal/shellreview/dialect"

type Domain string

const (
	NativeCLI        Domain = "native_cli"
	ShellBuiltin     Domain = "shell_builtin"
	PowerShellCmdlet Domain = "powershell_cmdlet"
)

type IdentitySource string

const (
	SystemProgram          IdentitySource = "system"
	ContainerSystemProgram IdentitySource = "container_system"
	PlatformProgram        IdentitySource = "platform"
	BuiltinProgram         IdentitySource = "builtin"
)

type CommandIdentity struct {
	Domain Domain
	Source IdentitySource
	Path   string
}

func (c CommandIdentity) Known() bool { return c.Domain != "" && c.Source != "" }

// CommandInput requires an identity proof established against the actual
// execution environment. Name lookup alone never creates that proof.
type CommandInput struct {
	Name     string
	Args     []string
	Words    []SourceWord
	Identity CommandIdentity
	Dialect  dialect.Dialect
}
