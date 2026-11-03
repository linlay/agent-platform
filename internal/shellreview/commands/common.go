package commands

import (
	"agent-platform/internal/shellreview/types"
	"strings"
)

func escalateWrites(e *effectDraft) {
	// An unrecognized option may turn read operands into output targets.
	for i := range e.Files {
		e.Files[i].Write = true
	}
}

func unknownOperands(args []string) effectDraft {
	e := effectDraft{Incomplete: true, ExecutesCode: true}
	for i, p := range args {
		ref := types.ArgumentRef(i, p)
		if value, ok := strings.CutPrefix(p, "--"); ok {
			if _, v, has := strings.Cut(value, "="); has {
				ref.Start = len(p) - len(v)
				p = v
			} else {
				continue
			}
		}
		if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: true, Ref: ref})
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
