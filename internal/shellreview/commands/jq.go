package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
	"regexp"
)

func jqOperands(args []string) effectDraft {
	p := options.Parse(jqSpec, args)
	e := effectDraft{Incomplete: p.Incomplete}
	e.Files = append(e.Files, p.Files...)
	positional := p.Positional
	refs := p.PositionalRefs
	if !p.Has("f") && !p.Has("from-file") && len(positional) > 0 {
		if jqModuleProgram.MatchString(positional[0]) {
			e.Deps = append(e.Deps, types.Dep{Kind: types.Script, Check: "inline-program"})
		}
		positional = positional[1:]
		refs = refs[1:]
	}
	if !p.Has("args") && !p.Has("jsonargs") {
		for i, path := range positional {
			if path != "-" {
				e.Files = append(e.Files, types.FileAccess{Path: path, Ref: refs[i]})
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	for _, key := range []string{"f", "from-file", "L"} {
		for _, path := range p.Values[key] {
			e.Deps = append(e.Deps, types.Dep{Kind: types.Script, Path: path, Check: "jq-program-or-module"})
		}
	}
	return e
}

var jqSpec = types.OptionSpec{ShortFlags: "rcsnejaSCMRh", ShortArgs: map[byte]types.OptionRole{'f': types.OptionRead, 'L': types.OptionReadDir},
	Long: options.Merge(options.Longs(types.OptionNone, "raw-output", "compact-output", "slurp", "null-input", "exit-status", "join-output", "ascii-output", "sort-keys", "color-output", "monochrome-output", "tab", "raw-input", "seq", "stream", "stream-errors", "raw-output0", "unbuffered", "args", "jsonargs"), options.Longs(types.OptionValue, "indent"), options.Longs(types.OptionRead, "from-file"), options.Longs(types.OptionTwoValues, "arg", "argjson"), options.Longs(types.OptionNameFile, "slurpfile", "rawfile"))}

var jqModuleProgram = regexp.MustCompile(`\b(import|include|module)\b`)
