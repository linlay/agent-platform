package commands

import (
	"agent-platform/internal/shellreview/types"
	"strings"
)

func escalateWrites(e *types.Effects) {
	// An unrecognized option may turn read operands into output targets.
	for i := range e.Files {
		e.Files[i].Write = true
	}
}

func unknownOperands(args []string) types.Effects {
	e := types.Effects{Incomplete: true, ExecutesCode: true}
	for _, p := range args {
		if value, ok := strings.CutPrefix(p, "--"); ok {
			if _, v, has := strings.Cut(value, "="); has {
				p = v
			} else {
				continue
			}
		}
		if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: true})
		}
	}
	return e
}

func configDisabled(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--no-config" {
			return true
		}
	}
	return false
}
