package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/runtimefs"
)

func environment(root, engineID string, lease *agentengine.Lease, proxy httpclient.Config) ([]string, error) {
	env := map[string]string{}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TMP", "TEMP"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	dirs := append([]string{filepath.Dir(lease.Command)}, lease.BinDirs...)
	if runtime.GOOS == "windows" {
		// Windows keys are case-insensitive. Canonicalize only the OS allowlist;
		// mixed-case NODE_OPTIONS and tokens never enter this environment.
		allowed := map[string]bool{"SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true, "TEMP": true, "TMP": true}
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			key = strings.ToUpper(key)
			if ok && allowed[key] {
				env[key] = value
			}
		}
		if !filepath.IsAbs(env["SYSTEMROOT"]) {
			return nil, errors.New("dependency_missing")
		}
		dirs = append(dirs, filepath.Join(env["SYSTEMROOT"], "System32"), env["SYSTEMROOT"])
	} else if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		dirs = append(dirs, "/usr/bin", "/bin")
	} else {
		return nil, errors.New("unsupported_deployment")
	}
	// Adapter subprocesses use user-installed CLIs. Preserve absolute search
	// directories after the managed runtime; never search the current Workspace.
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(directory) {
			dirs = append(dirs, directory)
		}
	}
	env["PATH"] = strings.Join(dirs, string(os.PathListSeparator))
	profile := filepath.Join(root, "agent-engine-profiles", profileID(engineID))
	// Profile identity belongs to the engine, not its program version or Chat.
	// Plugins own whether to use this root for state or a vendor config mapping.
	current := root
	relative, _ := filepath.Rel(root, profile)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtimefs.PrivateOwned(current) != nil {
			return nil, errors.New("profile_unavailable")
		}
	}
	env["ZENMIND_AGENT_PROFILE"] = profile
	if err := proxyEnvironment(env, proxy); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(env))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result, nil
}
func proxyEnvironment(env map[string]string, c httpclient.Config) error {
	resolver, err := httpclient.NewResolver(c)
	if err != nil {
		return errors.New("proxy_configuration_invalid")
	}
	proxy, err := resolver.ProxyEnvironment(context.Background())
	if err != nil {
		return err
	}
	for key, value := range proxy {
		env[key] = value
	}
	return nil
}

func profileID(engineID string) string {
	hash := sha256.Sum256([]byte(engineID))
	return hex.EncodeToString(hash[:16])
}
