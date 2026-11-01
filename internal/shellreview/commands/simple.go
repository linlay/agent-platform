package commands

import (
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
	"strings"
)

func simpleOperands(id registry.ID, args []string) types.Effects {
	e := types.Effects{}
	if id == registry.CmdCd {
		for _, p := range args {
			if !strings.HasPrefix(p, "-") {
				e.Files = append(e.Files, types.FileAccess{Path: p})
			}
		}
	}
	return e
}
