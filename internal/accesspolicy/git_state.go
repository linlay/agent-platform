package accesspolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellanalysis"
)

// gitStateTTL bounds reuse of a configuration scan whose watched files did not
// change. Pre-launch review re-validates the fingerprint of those files.
const gitStateTTL = 30 * time.Second

type gitStateEntry struct {
	fingerprint string
	clean       bool
	at          time.Time
}

var gitStateCache = struct {
	sync.Mutex
	entries map[string]gitStateEntry
}{entries: map[string]gitStateEntry{}}

// gitExecutionClean reports whether a Git invocation classified by
// shellanalysis runs no program implicitly: effective configuration defines no
// fsmonitor, diff driver, filter or signature program for reads, and for local
// writes additionally no hook, editor, credential helper or signing program.
func gitExecutionClean(session QuerySession, x BashExecution, dir string, check shellanalysis.GitCheck, vars map[string]string) bool {
	if session.AgentHasRuntimeSandbox || check == shellanalysis.GitCheckNone {
		return false
	}
	program := x.Program
	if program == "" {
		program = resolvedProgram("git", x.Cwd, vars)
	}
	if program == "" {
		return false
	}
	canonicalDir, err := NormalizePath(dir)
	if err != nil {
		return false
	}
	if info, err := os.Stat(canonicalDir); err != nil || !info.IsDir() {
		return false
	}
	env := environmentList(vars)
	gitDir, _ := runGit(program, canonicalDir, env, "rev-parse", "--absolute-git-dir")
	gitDir = strings.TrimSpace(gitDir)
	hooksDir := ""
	if check != shellanalysis.GitCheckRead && gitDir != "" {
		if out, err := runGit(program, canonicalDir, env, "rev-parse", "--git-path", "hooks"); err == nil {
			hooksDir = strings.TrimSpace(out)
			if hooksDir != "" && !filepath.IsAbs(hooksDir) {
				hooksDir = filepath.Join(canonicalDir, hooksDir)
			}
		}
	}
	key := strings.Join([]string{program, canonicalDir, string(check), vars["HOME"], vars["XDG_CONFIG_HOME"], vars["GIT_CONFIG_NOSYSTEM"]}, "\x00")
	fingerprint := gitStateFingerprint(gitDir, hooksDir, vars)
	gitStateCache.Lock()
	cached, ok := gitStateCache.entries[key]
	gitStateCache.Unlock()
	if ok && cached.fingerprint == fingerprint && time.Since(cached.at) < gitStateTTL {
		return cached.clean
	}
	clean := scanGitState(program, canonicalDir, env, check, hooksDir)
	gitStateCache.Lock()
	gitStateCache.entries[key] = gitStateEntry{fingerprint: fingerprint, clean: clean, at: time.Now()}
	gitStateCache.Unlock()
	return clean
}

func scanGitState(program, dir string, env []string, check shellanalysis.GitCheck, hooksDir string) bool {
	out, err := runGit(program, dir, env, "config", "--list", "-z")
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		key, value, _ := strings.Cut(entry, "\n")
		if gitKeyRunsProgram(strings.ToLower(key), strings.TrimSpace(value), check) {
			return false
		}
	}
	if check != shellanalysis.GitCheckRead && hooksDir != "" {
		entries, err := os.ReadDir(hooksDir)
		if err != nil && !os.IsNotExist(err) {
			return false
		}
		for _, item := range entries {
			if item.IsDir() || strings.HasSuffix(item.Name(), ".sample") {
				continue
			}
			info, err := item.Info()
			if err != nil {
				return false
			}
			if info.Mode()&0o111 != 0 || info.Mode()&os.ModeSymlink != 0 {
				return false
			}
		}
	}
	return true
}

func gitKeyRunsProgram(key, value string, check shellanalysis.GitCheck) bool {
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
	if check == shellanalysis.GitCheckRead {
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
	if check != shellanalysis.GitCheckNetwork {
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

func gitStateFingerprint(gitDir, hooksDir string, vars map[string]string) string {
	home := vars["HOME"]
	xdg := vars["XDG_CONFIG_HOME"]
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	files := []string{"/etc/gitconfig"}
	if home != "" {
		files = append(files, filepath.Join(home, ".gitconfig"))
	}
	if xdg != "" {
		files = append(files, filepath.Join(xdg, "git", "config"))
	}
	if gitDir != "" {
		files = append(files, filepath.Join(gitDir, "config"), filepath.Join(gitDir, "config.worktree"), filepath.Join(gitDir, "commondir"))
	}
	var b strings.Builder
	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			fmt.Fprintf(&b, "%s\x00%d\x00%d\x00", file, info.Size(), info.ModTime().UnixNano())
		}
	}
	if hooksDir != "" {
		if entries, err := os.ReadDir(hooksDir); err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if info, err := entry.Info(); err == nil {
					names = append(names, fmt.Sprintf("%s:%d:%o", entry.Name(), info.ModTime().UnixNano(), info.Mode()))
				}
			}
			sort.Strings(names)
			b.WriteString(strings.Join(names, ","))
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func runGit(program, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, append([]string{"--no-pager"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	return string(out), err
}

func environmentList(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for key, value := range vars {
		out = append(out, key+"="+value)
	}
	return out
}
