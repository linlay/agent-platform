package hostenv

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// WithSystemPaths appends standard system command directories that are missing
// from PATH. A Platform started from a desktop session inherits a minimal PATH
// that lacks the directories a login shell would add; no profile is executed.
func WithSystemPaths(env []string) []string {
	return AppendPath(env, systemPathDirs(runtime.GOOS)...)
}

// AppendPath adds existing directories to the end of PATH, keeping the
// original order and skipping directories that are already present.
func AppendPath(env []string, dirs ...string) []string {
	current := filepath.SplitList(Value(env, "PATH"))
	var extra []string
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		present := false
		for _, existing := range append(append([]string(nil), current...), extra...) {
			if samePath(existing, dir) {
				present = true
				break
			}
		}
		if !present {
			extra = append(extra, dir)
		}
	}
	if len(extra) == 0 {
		return env
	}
	joined := strings.Join(append(current, extra...), string(os.PathListSeparator))
	if len(current) == 0 || current[0] == "" && len(current) == 1 {
		joined = strings.Join(extra, string(os.PathListSeparator))
	}
	return Set(env, "PATH", joined)
}

func systemPathDirs(goos string) []string {
	switch goos {
	case "darwin":
		dirs := readPathFile("/etc/paths")
		if matches, err := filepath.Glob("/etc/paths.d/*"); err == nil {
			sort.Strings(matches)
			for _, file := range matches {
				dirs = append(dirs, readPathFile(file)...)
			}
		}
		// Homebrew's standard prefixes are added by its shellenv, not /etc/paths.
		return append(dirs, "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/sbin")
	case "linux":
		return []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}
	default:
		return nil
	}
}

func readPathFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var dirs []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" && !strings.HasPrefix(line, "#") {
			dirs = append(dirs, line)
		}
	}
	return dirs
}
