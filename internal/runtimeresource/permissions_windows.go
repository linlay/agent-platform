//go:build windows

package runtimeresource

import (
	"fmt"
	"os/exec"
	"os/user"
)

const windowsSystemSID = "*S-1-5-18"

func privateTreeACLArgs(root, identity string) []string {
	return []string{
		root,
		"/inheritance:r",
		"/grant:r",
		identity + ":F",
		identity + ":(OI)(CI)(IO)F",
		windowsSystemSID + ":F",
		windowsSystemSID + ":(OI)(CI)(IO)F",
		"/T",
		"/C",
	}
}

func securePrivateTree(root string) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user for runtime resource ACL: %w", err)
	}
	identity := current.Username
	if current.Uid != "" {
		identity = "*" + current.Uid
	}
	command := exec.Command("icacls", privateTreeACLArgs(root, identity)...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("restrict runtime resource ACL: %w: %s", err, string(output))
	}
	return nil
}
