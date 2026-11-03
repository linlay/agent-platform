package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/shellenv"
	"agent-platform/internal/shellreview/types"
)

// ReviewGitExecution scans effective Git configuration afresh on every review.
// Configuration queries do not run hooks/helpers. Startup injection is rejected
// before creating a subprocess, and results never cross execution environments.
func (i *DependencyInspector) ReviewGitExecution(x types.BashExecution, dir string, check types.GitCheck, priorMutation bool) DependencyReview {
	result := DependencyReview{State: types.Unresolved, Unsafe: true, Reason: "Git configuration cannot be confirmed"}
	if i.sandbox || check == types.GitCheckNone || len(x.StartupDeps) > 0 {
		return result
	}
	for name, value := range x.Env {
		if value != "" && shellenv.StartupOverride(name) {
			return result
		}
	}
	program := x.CommandIdentity.Path
	if program == "" {
		program = x.Program
	}
	if program == "" {
		return result
	}
	canonicalDir, err := NormalizePath(dir)
	if err != nil {
		return result
	}
	if info, err := os.Stat(canonicalDir); err != nil || !info.IsDir() {
		return result
	}
	env := gitProbeEnvironment(x.Env)
	// Never pass argv-supplied aliases, shell snippets or config overrides to a
	// probe. Their original argv remains opaque and is bound by the outer plan.
	out, err := runGit(program, canonicalDir, env, "config", "--includes", "--show-origin", "--list", "-z")
	if err != nil {
		return result
	}
	entries, err := parseGitConfigOrigins(out)
	if err != nil {
		return result
	}
	versions := []dependencyVersion{}
	clean := true
	for _, entry := range entries {
		if gitKeyRunsProgram(strings.ToLower(entry.key), strings.TrimSpace(entry.value), check) {
			clean = false
		}
		if !strings.HasPrefix(entry.origin, "file:") {
			continue
		}
		file := dependencyPath(strings.TrimPrefix(entry.origin, "file:"), canonicalDir)
		permission, err := i.permission(file)
		if err != nil {
			return result
		}
		if permission.HardReason != "" {
			return DependencyReview{State: types.HardViolation, Unsafe: true, Reason: permission.HardReason}
		}
		info, err := i.inspect(file)
		if err != nil || !info.Exists {
			return result
		}
		actual, err := i.permission(info.Canonical)
		if err != nil {
			return result
		}
		if actual.HardReason != "" {
			return DependencyReview{State: types.HardViolation, Unsafe: true, Reason: actual.HardReason}
		}
		versions = append(versions, dependencyVersion{Path: file, Info: info, State: "known"})
	}
	hooksDir := ""
	if check != types.GitCheckRead {
		output, err := runGit(program, canonicalDir, env, "rev-parse", "--git-path", "hooks")
		if err == nil {
			hooksDir = strings.TrimSpace(output)
			if hooksDir != "" {
				hooksDir = dependencyPath(hooksDir, canonicalDir)
			}
		}
		if hooksDir != "" {
			permission, err := i.permission(hooksDir)
			if err != nil {
				return result
			}
			if permission.HardReason != "" {
				return DependencyReview{State: types.HardViolation, Unsafe: true, Reason: permission.HardReason}
			}
			items, err := os.ReadDir(hooksDir)
			if len(items) > 4096 {
				return result
			}
			if err != nil && !os.IsNotExist(err) {
				return result
			}
			for _, item := range items {
				if item.IsDir() || strings.HasSuffix(item.Name(), ".sample") {
					continue
				}
				info, err := item.Info()
				if err != nil {
					return result
				}
				if info.Mode()&0111 == 0 && info.Mode()&os.ModeSymlink == 0 {
					continue
				}
				clean = false
				file := filepath.Join(hooksDir, item.Name())
				permission, err := i.permission(file)
				if err != nil {
					return result
				}
				if permission.HardReason != "" {
					return DependencyReview{State: types.HardViolation, Unsafe: true, Reason: permission.HardReason}
				}
				observed, err := i.inspect(file)
				if err != nil || !observed.Exists {
					return result
				}
				actual, err := i.permission(observed.Canonical)
				if err != nil {
					return result
				}
				if actual.HardReason != "" {
					return DependencyReview{State: types.HardViolation, Unsafe: true, Reason: actual.HardReason}
				}
				versions = append(versions, dependencyVersion{Path: file, Info: observed, State: fmt.Sprint(info.Mode())})
			}
		}
	}
	// Earlier code may create config/hooks after this observation. Do not reuse
	// current clean state as proof of the state at this later invocation.
	if priorMutation {
		clean = false
	}
	data, _ := json.Marshal(struct {
		Effective string
		Hooks     string
		Files     []dependencyVersion
	}{out, hooksDir, versions})
	sum := sha256.Sum256(data)
	return DependencyReview{State: types.Known, Unsafe: !clean, SHA256: hex.EncodeToString(sum[:]), Reason: "Git effective configuration and hooks are bound to this content version"}
}

type gitConfigEntry struct{ origin, key, value string }

func parseGitConfigOrigins(raw string) ([]gitConfigEntry, error) {
	if raw == "" {
		return nil, nil
	}
	fields := strings.Split(raw, "\x00")
	if fields[len(fields)-1] != "" || len(fields)%2 != 1 {
		return nil, fmt.Errorf("invalid Git config protocol")
	}
	var result []gitConfigEntry
	for j := 0; j < len(fields)-1; j += 2 {
		key, value, _ := strings.Cut(fields[j+1], "\n")
		if key == "" {
			return nil, fmt.Errorf("missing Git config key")
		}
		result = append(result, gitConfigEntry{fields[j], key, value})
	}
	return result, nil
}

func gitKeyRunsProgram(key, value string, check types.GitCheck) bool {
	falsy := value == "" || strings.EqualFold(value, "false") || value == "0" || strings.EqualFold(value, "no") || strings.EqualFold(value, "off")
	switch {
	case key == "core.fsmonitor":
		// "true" selects Git's built-in daemon; any other value names a hook program.
		return !falsy && !strings.EqualFold(value, "true")
	case key == "diff.external":
		return !falsy
	case strings.HasPrefix(key, "diff.") && (strings.HasSuffix(key, ".textconv") || strings.HasSuffix(key, ".command")):
		return !falsy
	case strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".smudge") || strings.HasSuffix(key, ".process")):
		return !falsy
	case key == "log.showsignature":
		return !falsy
	}
	if check == types.GitCheckRead {
		return false
	}
	switch {
	case key == "core.hookspath", key == "core.editor", key == "sequence.editor", key == "gpg.program":
		return !falsy
	case key == "commit.gpgsign", key == "tag.gpgsign", key == "push.gpgsign":
		return !falsy
	case strings.HasPrefix(key, "gpg.") && strings.HasSuffix(key, ".program"):
		return !falsy
	case strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver"):
		return !falsy
	}
	if check != types.GitCheckNetwork {
		return false
	}
	switch {
	case key == "core.sshcommand", key == "core.askpass":
		return !falsy
	case key == "credential.helper" || strings.HasPrefix(key, "credential.") && strings.HasSuffix(key, ".helper"):
		return !falsy && !standardCredentialHelper(value)
	}
	return false
}

// standardCredentialHelper accepts the helpers shipped with Git and the major
// platform keychains; "!command" and paths run arbitrary programs.
func standardCredentialHelper(value string) bool {
	name := strings.Fields(value)
	if len(name) == 0 {
		return true
	}
	switch name[0] {
	case "osxkeychain", "manager", "manager-core", "wincred", "libsecret", "cache", "store", "gnome-keyring":
		return true
	}
	return false
}

func runGit(program, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, append([]string{"--no-pager"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 100 * time.Millisecond
	var out boundedProbeOutput
	out.limit = maxDependencyBytes
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

func environmentList(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for key, value := range vars {
		out = append(out, key+"="+value)
	}
	return out
}

// Drop tracing, loaders, shell startup, editors and helper selection from probes.
// Only repository/config lookup variables affect these read-only Git builtins.
func gitProbeEnvironment(vars map[string]string) []string {
	filtered := map[string]string{"PATH": "/usr/bin:/bin", "LC_ALL": "C"}
	for key, value := range vars {
		switch key {
		case "HOME", "XDG_CONFIG_HOME", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS":
			filtered[key] = value
		default:
			if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
				filtered[key] = value
			}
		}
	}
	return environmentList(filtered)
}
