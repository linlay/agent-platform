package commands

import (
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
	"strings"
)

func simpleOperands(id registry.ID, args []string) effectDraft {
	e := effectDraft{}
	if id == registry.CmdCd {
		for i, p := range args {
			if !strings.HasPrefix(p, "-") {
				e.Files = append(e.Files, types.FileAccess{Path: p, Ref: types.ArgumentRef(i, p)})
			}
		}
	}
	return e
}
