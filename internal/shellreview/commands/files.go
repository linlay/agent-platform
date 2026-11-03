package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

var duSpec = types.OptionSpec{ShortFlags: "achksxLHbmPl0", ShortArgs: map[byte]types.OptionRole{'d': types.OptionValue, 'B': types.OptionValue, 't': types.OptionValue, 'X': types.OptionRead},
	Long: options.Merge(options.Longs(types.OptionNone, "summarize", "human-readable", "all", "total", "apparent-size", "si", "bytes", "dereference", "one-file-system", "count-links", "null", "inodes"), options.Longs(types.OptionValue, "max-depth", "exclude", "threshold", "block-size"), options.Longs(types.OptionOptionalValue, "time"), options.Longs(types.OptionRead, "exclude-from", "files0-from"))}

var grepSpec = types.OptionSpec{ShortFlags: "abcEFGhHiIlLnoqrRsUvwxyzZP", ShortArgs: map[byte]types.OptionRole{'e': types.OptionValue, 'f': types.OptionRead, 'm': types.OptionValue, 'A': types.OptionValue, 'B': types.OptionValue, 'C': types.OptionValue, 'd': types.OptionValue, 'D': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "count", "files-with-matches", "files-without-match", "word-regexp", "line-regexp", "fixed-strings", "extended-regexp", "perl-regexp", "basic-regexp", "invert-match", "only-matching", "no-filename", "with-filename", "quiet", "silent", "no-messages", "text", "null", "null-data", "line-number", "ignore-case", "no-ignore-case", "byte-offset", "initial-tab", "line-buffered"), options.Longs(types.OptionRecursive, "recursive", "dereference-recursive"), options.Longs(types.OptionOptionalValue, "color", "colour"), options.Longs(types.OptionValue, "include", "exclude", "exclude-dir", "binary-files", "max-count", "after-context", "before-context", "context", "regexp", "label", "devices", "directories"), options.Longs(types.OptionRead, "file", "exclude-from"))}

var lsSpec = types.OptionSpec{ShortFlags: "aAbBcCdDfFghHiklLmNnopqQrRsStuUvxXZ1", ShortArgs: map[byte]types.OptionRole{'I': types.OptionValue, 'w': types.OptionValue, 'T': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "all", "almost-all", "human-readable", "recursive", "reverse", "classify", "directory", "group-directories-first", "inode", "size", "full-time", "numeric-uid-gid", "dereference", "si", "literal", "quote-name", "escape", "hide-control-chars", "show-control-chars", "no-group", "author"), options.Longs(types.OptionOptionalValue, "color", "classify", "hyperlink", "indicator-style"), options.Longs(types.OptionValue, "sort", "time", "format", "time-style", "ignore", "hide", "width", "tabsize", "block-size", "quoting-style"))}

var uniqSpec = types.OptionSpec{ShortFlags: "cduizD", ShortArgs: map[byte]types.OptionRole{'f': types.OptionValue, 's': types.OptionValue, 'w': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "count", "repeated", "unique", "ignore-case", "zero-terminated"), options.Longs(types.OptionOptionalValue, "all-repeated", "group"), options.Longs(types.OptionValue, "skip-fields", "skip-chars", "check-chars"))}

func fileOperands(id registry.ID, spec types.OptionSpec, args []string) effectDraft {
	p := options.Parse(spec, args)
	e := effectDraft{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool, ref types.ValueRef) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: write, Recursive: recursive, Ref: ref, Kind: types.Read})
		}
	}
	recursive := p.Recursive || p.Has("r") || p.Has("R")
	switch id {
	case registry.CmdDu, registry.CmdLs:
		if id == registry.CmdDu {
			recursive = true
		}
		for i, path := range p.Positional {
			add(path, false, recursive, p.PositionalRefs[i])
		}
		if len(p.Positional) == 0 {
			add(".", false, recursive, types.ValueRef{})
		}
	case registry.CmdUniq:
		if len(p.Positional) > 0 {
			add(p.Positional[0], false, false, p.PositionalRefs[0])
		}
		if len(p.Positional) > 1 {
			add(p.Positional[1], true, false, p.PositionalRefs[1])
			e.Files[len(e.Files)-1].Kind = types.Output
		}
	case registry.CmdGrep, registry.CmdRg:
		positional := p.Positional
		refs := p.PositionalRefs
		if !p.Has("e") && !p.Has("regexp") && !p.Has("f") && !p.Has("file") && !(id == registry.CmdRg && (p.Has("files") || p.Has("type-list"))) && len(positional) > 0 {
			positional = positional[1:]
			refs = refs[1:]
		}
		if id == registry.CmdRg {
			recursive = true
		}
		for i, path := range positional {
			add(path, false, recursive, refs[i])
		}
		if len(positional) == 0 && recursive {
			add(".", false, true, types.ValueRef{})
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
