package bashsec

import (
	"fmt"
	"regexp"
	"strings"
)

// validateZshDangerousCommands blocks Zsh-specific commands that can bypass
// security checks: zmodload, emulate, sysopen, sysread, syswrite, zpty, ztcp,
// zsocket, zf_* builtins, and fc -e.
func validateZshDangerousCommands(command string) (bool, string) {
	trimmed := strings.TrimSpace(command)
	tokens := strings.Fields(trimmed)
	baseCmd := ""
	for _, tok := range tokens {
		if len(tok) > 0 && isVarAssignment(tok) {
			continue
		}
		if zshPrecommandModifiers[tok] {
			continue
		}
		baseCmd = tok
		break
	}

	if zshDangerousCommands[baseCmd] {
		return false, fmt.Sprintf("Command uses Zsh-specific '%s' which can bypass security checks", baseCmd)
	}

	if baseCmd == "fc" {
		fcDashE := regexp.MustCompile(`\s-\S*e`)
		if fcDashE.MatchString(trimmed) {
			return false, "Command uses 'fc -e' which can execute arbitrary commands via editor"
		}
	}

	return true, ""
}

// isVarAssignment checks if a token looks like a shell variable assignment (VAR=value).
func isVarAssignment(tok string) bool {
	return varAssignRe.MatchString(tok)
}
