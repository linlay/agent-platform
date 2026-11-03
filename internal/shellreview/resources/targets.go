package resources

import (
	"agent-platform/internal/pathutil"
	"agent-platform/internal/shellreview/types"
	"os"
	"path/filepath"
	"strings"
)

func targetAt(raw, cwd string) string {
	if raw == "" || filepath.IsAbs(raw) || strings.HasPrefix(raw, "~") {
		return raw
	}
	return pathutil.JoinUnclean(cwd, raw)
}
func physicalParent(raw string) string {
	raw = strings.TrimRight(raw, "/")
	i := strings.LastIndexByte(raw, '/')
	if i < 0 {
		return "."
	}
	if i == 0 {
		return "/"
	}
	return raw[:i]
}

// TargetPaths keeps both possible file/directory destinations without probing
// the Host filesystem for a container target. Joins preserve symlink/.. order.
func TargetPaths(op types.Op, target types.Target, cwd string, sandbox bool, env *types.BashEnvironment) []string {
	raw := targetAt(target.Path, cwd)
	if target.RelativeToDestination && !filepath.IsAbs(target.Path) {
		var paths []string
		for _, destination := range op.Targets {
			if destination.Role != types.Destination {
				continue
			}
			for _, p := range TargetPaths(op, destination, cwd, sandbox, env) {
				paths = append(paths, pathutil.JoinUnclean(physicalParent(p), target.Path))
			}
		}
		if len(paths) > 0 {
			return paths
		}
	}
	switch target.Placement {
	case types.IntoDirectory:
		return []string{pathutil.JoinUnclean(raw, target.Child)}
	case types.FileOrDirectory:
		switch transferPathKind(raw, sandbox, env) {
		case types.PathDirectory:
			return []string{pathutil.JoinUnclean(raw, target.Child)}
		case types.PathNonDirectory:
			return []string{raw}
		default:
			return []string{raw, pathutil.JoinUnclean(raw, target.Child)}
		}
	case types.ParentDirectory:
		return []string{physicalParent(raw), raw}
	default:
		return []string{raw}
	}
}

func transferPathKind(raw string, sandbox bool, env *types.BashEnvironment) types.PathKind {
	if sandbox {
		if env != nil && env.PathKind != nil {
			if kind, err := env.PathKind(raw); err == nil {
				return kind
			}
		}
		return types.PathUnknown
	}
	if info, err := os.Stat(raw); err == nil {
		if info.IsDir() {
			return types.PathDirectory
		}
		return types.PathNonDirectory
	}
	return types.PathUnknown
}
