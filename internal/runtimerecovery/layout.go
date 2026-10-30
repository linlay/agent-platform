package runtimerecovery

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This first contract supports the fixed Desktop layout only. Custom storage
// roots are rejected rather than silently leaving user data behind. This reads
// declarations only; it never sources shell code or rewrites service config.
func validateLayout(configDir, source string) error {
	if configDir == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(configDir, ".env"))
	if os.IsNotExist(err) {
		return nil
	} // A missing config is a valid repair case.
	if err != nil {
		return err
	}
	defer file.Close()
	defaults := map[string]string{"AP_RUNTIME_DIR": "", "AP_RUNTIME_CHATS_DIR": "chats", "AP_RUNTIME_MEMORY_DIR": "memory", "AP_RUNTIME_PAN_DIR": "pan", "AP_RUNTIME_REGISTRIES_DIR": "registries", "AP_RUNTIME_STATE_DIR": ".state"}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		relative, relevant := defaults[key]
		if !ok || !relevant {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if value == "" {
			continue
		}
		resolved := filepath.Clean(value)
		if canonical, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = canonical
		} else if parent, err := filepath.EvalSymlinks(filepath.Dir(resolved)); err == nil {
			resolved = filepath.Join(parent, filepath.Base(resolved))
		}
		expected := filepath.Join(source, relative)
		matches := resolved == expected
		if runtime.GOOS == "windows" {
			matches = strings.EqualFold(resolved, expected)
		}
		if !filepath.IsAbs(value) || !matches {
			return fmt.Errorf("runtime recovery v1 supports the fixed Desktop layout only; unsupported %s declaration", key)
		}
	}
	return scanner.Err()
}
