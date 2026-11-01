package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

var chmodSpec = types.OptionSpec{ShortFlags: "Rfvc", Long: options.Merge(options.Longs(types.OptionNone, "silent", "quiet", "verbose", "changes", "preserve-root", "no-preserve-root"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionRead, "reference"))}

var cpSpec = types.OptionSpec{ShortFlags: "afinpPRrsTvuLHlxb", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "archive", "force", "interactive", "no-clobber", "recursive", "symbolic-link", "verbose", "no-target-directory", "parents", "dereference", "no-dereference", "link", "remove-destination", "strip-trailing-slashes", "one-file-system", "attributes-only"), options.Longs(types.OptionOptionalValue, "preserve", "update", "backup", "reflink", "sparse", "no-preserve"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

var lnSpec = types.OptionSpec{ShortFlags: "sfnvTbrLPi", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "symbolic", "force", "no-dereference", "verbose", "relative", "logical", "physical", "interactive", "no-target-directory"), options.Longs(types.OptionOptionalValue, "backup"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

var mkdirSpec = types.OptionSpec{ShortFlags: "pv", ShortArgs: map[byte]types.OptionRole{'m': types.OptionValue}, Long: options.Merge(options.Longs(types.OptionNone, "parents", "verbose"), options.Longs(types.OptionValue, "mode", "context"))}

var mvSpec = types.OptionSpec{ShortFlags: "finvuTb", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "force", "interactive", "no-clobber", "verbose", "no-target-directory", "strip-trailing-slashes"), options.Longs(types.OptionOptionalValue, "update", "backup"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

var rmSpec = types.OptionSpec{ShortFlags: "fiIdvRr", Long: options.Merge(options.Longs(types.OptionNone, "force", "dir", "verbose", "one-file-system", "preserve-root"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionOptionalValue, "interactive"))}

var rmdirSpec = types.OptionSpec{ShortFlags: "pv", Long: options.Longs(types.OptionNone, "parents", "ignore-fail-on-non-empty", "verbose")}

var teeSpec = types.OptionSpec{ShortFlags: "aip", Long: options.Merge(options.Longs(types.OptionNone, "append", "ignore-interrupts"), options.Longs(types.OptionOptionalValue, "output-error"))}

var touchSpec = types.OptionSpec{ShortFlags: "acmfh", ShortArgs: map[byte]types.OptionRole{'r': types.OptionRead, 'd': types.OptionValue, 't': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "no-create", "no-dereference"), options.Longs(types.OptionValue, "date", "time"), options.Longs(types.OptionRead, "reference"))}

func fileOperationOperands(id registry.ID, spec types.OptionSpec, args []string) types.Effects {
	p := options.Parse(spec, args)
	e := types.Effects{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: write, Recursive: recursive})
		}
	}
	recursive := p.Recursive || p.Has("r") || p.Has("R")
	switch id {
	case registry.CmdMkdir, registry.CmdTouch, registry.CmdTee, registry.CmdChmod, registry.CmdRmdir:
		positional := p.Positional
		if id == registry.CmdChmod && !p.Has("reference") && len(positional) > 0 {
			positional = positional[1:]
		}
		for _, path := range positional {
			add(path, true, recursive && id == registry.CmdChmod)
		}
	case registry.CmdRm:
		for _, path := range p.Positional {
			add(path, true, recursive)
		}
		e.Destructive = recursive
	case registry.CmdCp, registry.CmdMv, registry.CmdLn:
		targetGiven := p.Has("t") || p.Has("target-directory")
		for i, path := range p.Positional {
			isTarget := !targetGiven && i == len(p.Positional)-1 && len(p.Positional) > 1
			switch id {
			case registry.CmdMv:
				add(path, true, true)
			case registry.CmdCp:
				add(path, isTarget, recursive || p.Has("a") || p.Has("archive"))
			case registry.CmdLn:
				// ln TARGET LINK: the link location is written; the target is only named.
				if isTarget || len(p.Positional) == 1 {
					add(path, true, false)
				}
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
