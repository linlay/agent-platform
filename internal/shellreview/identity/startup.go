package identity

import (
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellenv"
	"agent-platform/internal/shellreview/types"
)

// StartupDependencies captures each native wrapper's environment before it can
// unset values for a later process. This function never opens or executes paths.
func StartupDependencies(vars map[string]string, cwd string) []types.Dep {
	var names []string
	for name, value := range vars {
		if value != "" && shellenv.StartupOverride(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var deps []types.Dep
	for _, name := range names {
		value := vars[name]
		deps = append(deps, types.Dep{Kind: types.Environment, Variable: name, Path: value, Check: "startup"})
		if strings.HasPrefix(name, "GIT_TRACE") && filepath.IsAbs(value) {
			deps = append(deps, types.Dep{Kind: types.EnvironmentOutput, Path: value})
		}
		if (name == "BASH_ENV" || name == "ENV" || name == "PYTHONSTARTUP") && !strings.ContainsAny(value, "$`~") {
			if !filepath.IsAbs(value) {
				value = pathutil.JoinUnclean(cwd, value)
			}
			deps = append(deps, types.Dep{Kind: types.Script, Path: value})
		}
	}
	return deps
}
