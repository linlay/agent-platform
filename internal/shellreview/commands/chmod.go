package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

var chmodSpec = types.OptionSpec{ShortFlags: "Rfvc", Long: options.Merge(options.Longs(types.OptionNone, "silent", "quiet", "verbose", "changes", "preserve-root", "no-preserve-root"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionRead, "reference"))}

func chmodOperands(args []string) effectDraft {
	p := options.Parse(chmodSpec, args)
	e := effectDraft{Files: p.Files, Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	positional, refs := p.Positional, p.PositionalRefs
	// The first operand is the mode unless --reference supplies it.
	if !p.Has("reference") && len(positional) > 0 {
		positional, refs = positional[1:], refs[1:]
	}
	recursive := p.Recursive || p.Has("R")
	for i, path := range positional {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: true, Recursive: recursive, Ref: refs[i], Kind: types.Chmod})
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
