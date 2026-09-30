package shellenv

import "strings"

// UnsafeOverride is shared by Agent, Skill, invocation and run.env validation.
// None of these channels may silently alter the program or shell being reviewed.
func UnsafeOverride(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if Reserved(n) {
		return true
	}
	if strings.HasPrefix(n, "LD_") || strings.HasPrefix(n, "DYLD_") || strings.HasPrefix(n, "GIT_CONFIG_") {
		return true
	}
	switch n {
	case "PATH", "PATHEXT", "COMSPEC", "SYSTEMROOT", "WINDIR", "HOME", "TMPDIR", "TMP", "TEMP",
		"ZDOTDIR", "IFS", "PS1", "PS2", "PS3", "PS4", "PROMPT", "HISTFILE",
		"NODE_OPTIONS", "NODE_PATH", "PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONINSPECT",
		"RUBYOPT", "RUBYLIB", "PERL5OPT", "PERL5LIB", "PERLLIB", "LUA_INIT", "LUA_PATH", "LUA_CPATH",
		"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "SSH_ASKPASS",
		"GIT_EXEC_PATH", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "EDITOR", "VISUAL", "PAGER", "LESSOPEN", "LESSCLOSE":
		return true
	}
	return false
}

// InheritedEnvironment deliberately inherits a small portable set. Secrets and
// interpreter/shell startup knobs in the Platform process do not reach tools.
func InheritedEnvironment(env []string) []string {
	var result []string
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		n := strings.ToUpper(key)
		switch n {
		case "PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"TMPDIR", "TMP", "TEMP", "USER", "USERNAME", "LOGNAME", "LANG", "LANGUAGE", "TZ", "TERM", "COLORTERM",
			"APPDATA", "LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMDATA":
			result = append(result, item)
		default:
			if strings.HasPrefix(n, "LC_") {
				result = append(result, item)
			}
		}
	}
	return result
}
