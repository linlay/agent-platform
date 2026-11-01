package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

func jqOperands(args []string) types.Effects {
	p := options.Parse(jqSpec, args)
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

var jqSpec = types.OptionSpec{ShortFlags: "rcsnejaSCMRh", ShortArgs: map[byte]types.OptionRole{'f': types.OptionRead, 'L': types.OptionReadDir},
	Long: options.Merge(options.Longs(types.OptionNone, "raw-output", "compact-output", "slurp", "null-input", "exit-status", "join-output", "ascii-output", "sort-keys", "color-output", "monochrome-output", "tab", "raw-input", "seq", "stream", "stream-errors", "raw-output0", "unbuffered", "args", "jsonargs"), options.Longs(types.OptionValue, "indent"), options.Longs(types.OptionRead, "from-file"), options.Longs(types.OptionTwoValues, "arg", "argjson"), options.Longs(types.OptionNameFile, "slurpfile", "rawfile"))}
