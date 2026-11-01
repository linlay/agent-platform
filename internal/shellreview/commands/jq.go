package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

func jqOperands(args []string) types.Effects {
	p := options.Parse(specs["jq"], args)
	e := types.Effects{Incomplete: p.Incomplete}
	e.Files = append(e.Files, p.Files...)
	positional := p.Positional
	if !p.Has("f") && !p.Has("from-file") && len(positional) > 0 {
		positional = positional[1:]
	}
	if !p.Has("args") && !p.Has("jsonargs") {
		for _, path := range positional {
			if path != "-" {
				e.Files = append(e.Files, types.FileAccess{Path: path})
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
