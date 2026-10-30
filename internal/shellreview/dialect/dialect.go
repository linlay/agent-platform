// Package dialect names execution grammars without depending on runtime policy.
package dialect

type Dialect string

const (
	Unknown    Dialect = "unknown"
	Bash       Dialect = "bash"
	PowerShell Dialect = "powershell"
	CMD        Dialect = "cmd"
	MSYS       Dialect = "msys-bash"
)
