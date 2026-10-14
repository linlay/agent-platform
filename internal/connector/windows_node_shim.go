package connector

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrUnsupportedWindowsNodeShim = errors.New("unsupported CLI shim")

var windowsNodeShimPattern = regexp.MustCompile(`(?i)"%(?:~dp0|dp0%)[\\/]?([^"\r\n]+\.(?:js|cjs|mjs))"`)

// WindowsNodeShimScript recognizes only npm's literal script operand. The
// launcher is never evaluated as shell source or allowed to rewrite user argv.
func WindowsNodeShimScript(data string) (string, error) {
	matches := windowsNodeShimPattern.FindAllStringSubmatch(data, -1)
	target := ""
	for _, m := range matches {
		if strings.ContainsAny(m[1], "%!\x00") || (target != "" && target != m[1]) {
			return "", fmt.Errorf("ambiguous CLI shim")
		}
		target = m[1]
	}
	if target == "" {
		return "", ErrUnsupportedWindowsNodeShim
	}
	return target, nil
}

// WindowsNodeShimTarget is shared by the frozen entry snapshot and execution.
// It resolves the actual script while retaining npm's installation boundary.
func WindowsNodeShimTarget(entry string) (string, error) {
	data, err := os.ReadFile(entry)
	if err != nil || len(data) > 65536 {
		return "", fmt.Errorf("unsupported CLI launcher")
	}
	script, err := WindowsNodeShimScript(string(data))
	if err != nil {
		return "", err
	}
	target := filepath.Join(filepath.Dir(entry), filepath.FromSlash(strings.ReplaceAll(script, "\\", "/")))
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(entry))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("CLI shim escapes installation")
	}
	return target, nil
}
