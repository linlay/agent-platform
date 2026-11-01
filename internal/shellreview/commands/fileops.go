package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

var chmodSpec = types.OptionSpec{ShortFlags: "Rfvc", Long: options.Merge(options.Longs(types.OptionNone, "silent", "quiet", "verbose", "changes", "preserve-root", "no-preserve-root"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionRead, "reference"))}

var mkdirSpec = types.OptionSpec{ShortFlags: "pv", ShortArgs: map[byte]types.OptionRole{'m': types.OptionValue}, Long: options.Merge(options.Longs(types.OptionNone, "parents", "verbose"), options.Longs(types.OptionValue, "mode", "context"))}

var rmSpec = types.OptionSpec{ShortFlags: "fiIdvRr", Long: options.Merge(options.Longs(types.OptionNone, "force", "dir", "verbose", "one-file-system", "preserve-root"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionOptionalValue, "interactive"))}

var rmdirSpec = types.OptionSpec{ShortFlags: "pv", Long: options.Longs(types.OptionNone, "parents", "ignore-fail-on-non-empty", "verbose")}

var teeSpec = types.OptionSpec{ShortFlags: "aip", Long: options.Merge(options.Longs(types.OptionNone, "append", "ignore-interrupts"), options.Longs(types.OptionOptionalValue, "output-error"))}

var touchSpec = types.OptionSpec{ShortFlags: "acmfh", ShortArgs: map[byte]types.OptionRole{'r': types.OptionRead, 'd': types.OptionValue, 't': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "no-create", "no-dereference"), options.Longs(types.OptionValue, "date", "time"), options.Longs(types.OptionRead, "reference"))}

func fileOperationOperands(id registry.ID, spec types.OptionSpec, args []string) effectDraft {
	p := options.Parse(spec, args)
	e := effectDraft{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool, ref types.ValueRef, kind types.OpKind) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: write, Recursive: recursive, Ref: ref, Kind: kind})
		}
	}
	recursive := p.Recursive || p.Has("r") || p.Has("R")
	switch id {
	case registry.CmdMkdir, registry.CmdTouch, registry.CmdTee, registry.CmdChmod, registry.CmdRmdir:
		positional := p.Positional
		refs := p.PositionalRefs
		if id == registry.CmdChmod && !p.Has("reference") && len(positional) > 0 {
			positional = positional[1:]
			refs = refs[1:]
		}
		for i, path := range positional {
			kind := types.Create
			if id == registry.CmdChmod {
				kind = types.Chmod
			}
			if id == registry.CmdRmdir {
				kind = types.Delete
			}
			if id == registry.CmdTee {
				kind = types.Output
			}
			add(path, true, recursive && id == registry.CmdChmod, refs[i], kind)
		}
	case registry.CmdRm:
		for i, path := range p.Positional {
			add(path, true, recursive, p.PositionalRefs[i], types.Delete)
		}
		e.Destructive = recursive

	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
