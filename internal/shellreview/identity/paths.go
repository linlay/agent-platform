package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/hostenv"
	"agent-platform/internal/pathutil"
)

func ResolvedProgram(name, cwd string, variables map[string]string) string {
	if runtime.GOOS == "windows" {
		// npm publishes both a POSIX shim without an extension and a Windows
		// cmd shim. Use Windows PATHEXT resolution, never the POSIX sibling.
		if strings.ContainsAny(name, "/\\") && !filepath.IsAbs(name) {
			name = ResolveAgainstCwd(name, cwd)
		}
		env := []string{"PATH=" + os.Getenv("PATH")}
		for key, value := range variables {
			if strings.EqualFold(key, "PATH") || strings.EqualFold(key, "PATHEXT") {
				env = hostenv.Set(env, key, value)
			}
		}
		if p, err := hostenv.LookPath(name, env); err == nil {
			return p
		}
		return ""
	}
	if strings.ContainsAny(name, "/\\") || filepath.IsAbs(name) {
		return ResolveAgainstCwd(name, cwd)
	}
	path := os.Getenv("PATH")
	if value, ok := variables["PATH"]; ok {
		path = value
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = cwd
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Only mapped container content may be inspected for run-authored provenance.
// /tmp keeps its historical path exception but is not a host-content mount.
func ExecutableHostPath(session QuerySession, path string) (string, bool) {
	if !session.AgentHasRuntimeSandbox {
		p, err := ResolveSessionPath(session, path)
		return p, err == nil
	}
	for _, roots := range []struct{ guest, host string }{
		{session.RuntimeContext.SandboxPaths.WorkspaceDir, SessionWorkspaceRoot(session)},
		{session.RuntimeContext.SandboxPaths.ChatDir, SessionChatDir(session)},
	} {
		if roots.guest == "" || roots.host == "" {
			continue
		}
		guest := strings.TrimRight(filepath.ToSlash(roots.guest), "/")
		if path == guest || strings.HasPrefix(filepath.ToSlash(path), guest+"/") {
			p, err := ResolveSessionPath(session, path)
			return p, err == nil
		}
		// Relative command operands have already been resolved to the frozen host cwd.
		p, err := pathutil.Canonicalize(path)
		r, rootErr := pathutil.Canonicalize(roots.host)
		if err == nil && rootErr == nil && pathutil.WithinRoot(p, r) {
			return p.Host, true
		}
	}
	return "", false
}
