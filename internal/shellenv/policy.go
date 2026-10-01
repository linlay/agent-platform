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

// defaultInherit is used when no configured list is supplied (internal callers).
var defaultInherit = []string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"TMPDIR", "TMP", "TEMP", "USER", "USERNAME", "LOGNAME", "LANG", "LANGUAGE", "LC_*", "TZ", "TERM", "COLORTERM",
	"APPDATA", "LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMDATA"}

// alwaysInheritable are platform/shell basics that UnsafeOverride forbids
// definitions from replacing but that must still flow from the host.
var alwaysInheritable = map[string]bool{"PATH": true, "PATHEXT": true, "COMSPEC": true, "SYSTEMROOT": true, "WINDIR": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true}

// InheritedEnvironment passes only listed host variables (NAME or PREFIX*) to
// tool processes. Loader, interpreter-preload and Git execution variables are
// never inherited even when listed; secrets in the Platform process stay out.
func InheritedEnvironment(env []string, names ...string) []string {
	if len(names) == 0 {
		names = defaultInherit
	}
	exact := map[string]bool{}
	var prefixes []string
	for _, name := range names {
		n := strings.ToUpper(strings.TrimSpace(name))
		if n == "" {
			continue
		}
		if prefix, ok := strings.CutSuffix(n, "*"); ok {
			prefixes = append(prefixes, prefix)
			continue
		}
		exact[n] = true
	}
	var result []string
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		n := strings.ToUpper(key)
		if UnsafeOverride(n) && !alwaysInheritable[n] {
			continue
		}
		listed := exact[n]
		for _, prefix := range prefixes {
			listed = listed || strings.HasPrefix(n, prefix)
		}
		if listed {
			result = append(result, item)
		}
	}
	return result
}
