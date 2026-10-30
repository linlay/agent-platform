package config

import (
	"fmt"
	"path/filepath"
	"strings"

	shellsyntax "agent-platform/internal/shellreview/syntax"
)

func validateBashShellConfig(bash BashConfig) error {
	if reason := shellsyntax.CheckText(bash.ShellExecutable); reason != "" {
		return fmt.Errorf("bash.shell-executable: %s", reason)
	}
	name := strings.TrimSpace(bash.ShellExecutable)
	if name != "" && !filepath.IsAbs(name) {
		// Accept Windows absolute forms even when validating a portable config
		// on another host. Drive-relative C:foo is not an executable name.
		drive := len(name) >= 2 && name[1] == ':' && (name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z')
		driveAbsolute := drive && len(name) >= 3 && (name[2] == '\\' || name[2] == '/')
		unc := strings.HasPrefix(name, `\\`) && len(strings.FieldsFunc(name[2:], func(r rune) bool { return r == '\\' || r == '/' })) >= 2
		if !driveAbsolute && !unc && (name == "." || name == ".." || drive || strings.ContainsAny(name, `/\`)) {
			return fmt.Errorf("bash.shell-executable must be an executable name or an absolute path; relative paths are not supported")
		}
	}
	for _, arg := range bash.ShellArgs {
		if reason := shellsyntax.CheckText(arg); reason != "" {
			return fmt.Errorf("bash.shell-args: %s", reason)
		}
	}
	return nil
}
