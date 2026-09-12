// Package hostenv discovers optional host command directories without changing
// the Platform process environment. It is never used for container commands.
package hostenv

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var npmCache = struct {
	sync.Mutex
	values map[[32]byte]string
}{values: map[[32]byte]string{}}

func Value(env []string, name string) string {
	var value string
	for _, item := range env {
		k, v, ok := strings.Cut(item, "=")
		if ok && (k == name || runtime.GOOS == "windows" && strings.EqualFold(k, name)) {
			value = v
		}
	}
	return value
}

// Set replaces all spellings of a variable on Windows.
func Set(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		k, _, _ := strings.Cut(item, "=")
		if k != key && !(runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			out = append(out, item)
		}
	}
	return append(out, key+"="+value)
}

// LookPath resolves against the supplied environment, never the caller's PATH.
func LookPath(name string, env []string) (string, error) {
	candidates := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		candidates = nil
		exts := Value(env, "PATHEXT")
		if exts == "" {
			exts = ".COM;.EXE;.BAT;.CMD"
		}
		for _, ext := range strings.Split(exts, ";") {
			candidates = append(candidates, name+strings.ToLower(ext))
		}
	}
	dirs := filepath.SplitList(Value(env, "PATH"))
	if filepath.IsAbs(name) {
		dirs = []string{""}
	} else if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("command must be absolute or a name: %s", name)
	}
	for _, dir := range dirs {
		if dir == "" && !filepath.IsAbs(name) {
			continue
		}
		for _, candidate := range candidates {
			p := candidate
			if !filepath.IsAbs(candidate) {
				if !filepath.IsAbs(dir) {
					continue
				}
				p = filepath.Join(dir, candidate)
			}
			info, err := os.Stat(p)
			if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("executable %s not found in PATH", name)
}

func npmBin(env []string) string {
	npm, err := LookPath("npm", env)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"prefix", "-g"}
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(npm), ".cmd") {
		entry := filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
		if _, err := os.Stat(entry); err != nil {
			return ""
		}
		npm, err = LookPath("node", env)
		if err != nil {
			return ""
		}
		args = append([]string{entry}, args...)
	}
	cmd := exec.CommandContext(ctx, npm, args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second
	data, err := cmd.Output()
	if err != nil {
		return ""
	}
	prefix := strings.TrimSpace(string(data))
	if !filepath.IsAbs(prefix) || strings.ContainsAny(prefix, "\r\n") {
		return ""
	}
	return npmGlobalBin(prefix, runtime.GOOS)
}

func npmGlobalBin(prefix, goos string) string {
	if goos != "windows" {
		return filepath.Join(prefix, "bin")
	}
	return prefix
}

// Refresh invalidates positive and negative probes after an explicit install.
func Refresh() { npmCache.Lock(); npmCache.values = map[[32]byte]string{}; npmCache.Unlock() }

func WithNPM(env []string) []string {
	sorted := append([]string(nil), env...)
	sort.Strings(sorted)
	key := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	npmCache.Lock()
	bin, ok := npmCache.values[key]
	if !ok {
		bin = npmBin(env)
		if len(npmCache.values) >= 128 {
			npmCache.values = map[[32]byte]string{}
		}
		npmCache.values[key] = bin
	}
	npmCache.Unlock()
	if bin == "" {
		return append([]string(nil), env...)
	}
	dirs := []string{bin}
	for _, p := range filepath.SplitList(Value(env, "PATH")) {
		if p != "" && !samePath(p, bin) {
			dirs = append(dirs, p)
		}
	}
	return Set(env, "PATH", strings.Join(dirs, string(os.PathListSeparator)))
}
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
