//go:build windows

package runtimeresource

import (
	"fmt"
	"os/exec"
	"os/user"
)

func securePrivateTree(root string) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user for runtime resource ACL: %w", err)
	}
	command := exec.Command(
		"icacls",
		root,
		"/inheritance:r",
		"/grant:r",
		current.Username+":(OI)(CI)F",
		"SYSTEM:(OI)(CI)F",
		"/T",
		"/C",
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("restrict runtime resource ACL: %w: %s", err, string(output))
	}
	return nil
}
