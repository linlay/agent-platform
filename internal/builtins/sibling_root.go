package builtins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultSourceRoot prefers the checkout's adjacent source repository. In a
// linked worktree, fall back to the main checkout's sibling, never an arbitrary
// PATH/search location. Explicit flags and environment overrides bypass this.
func DefaultSourceRoot(repoRoot, relative string) string {
	candidate := filepath.Clean(filepath.Join(repoRoot, relative))
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	if filepath.IsAbs(relative) {
		return candidate
	}
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return candidate
	}
	common := strings.TrimSpace(string(output))
	if !filepath.IsAbs(common) || filepath.Base(common) != ".git" {
		return candidate
	}
	mainCandidate := filepath.Clean(filepath.Join(filepath.Dir(common), relative))
	if info, err := os.Stat(mainCandidate); err == nil && info.IsDir() {
		return mainCandidate
	}
	return candidate
}
