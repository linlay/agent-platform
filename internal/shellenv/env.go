// Package shellenv holds the environment names owned by the managed host shell.
// It deliberately has no dependency on configuration or execution packages.
package shellenv

import "strings"

const GitBashExecutable = "AP_GIT_BASH_EXE"

// Reserved prevents definitions and run.env from changing shell initialization
// behind the access-policy review. HOME and temporary paths are also fixed by
// the managed shell so expansion and native tools agree on their identities.
func Reserved(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case GitBashExecutable, "MSYSTEM", "MSYSTEM_PREFIX", "MSYS", "MSYS2_PATH_TYPE",
		"MSYS2_ARG_CONV_EXCL", "MSYS2_ENV_CONV_EXCL", "MSYS_NO_PATHCONV",
		"CHERE_INVOKING", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS",
		"CDPATH", "GLOBIGNORE", "INPUTRC", "PROMPT_COMMAND", "BASH_XTRACEFD":
		return true
	}
	return strings.HasPrefix(strings.ToUpper(name), "BASH_FUNC_")
}

// StripLocator prevents an inherited shell locator leaking into an execution
// channel that has not opted into managed Git Bash.
func StripLocator(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, GitBashExecutable) {
			out = append(out, entry)
		}
	}
	return out
}
