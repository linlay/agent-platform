package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

var catSpec = types.OptionSpec{ShortFlags: "AbenstuvET", Long: options.Longs(types.OptionNone, "number", "number-nonblank", "show-all", "show-ends", "show-tabs", "squeeze-blank", "show-nonprinting")}

var cutSpec = types.OptionSpec{ShortFlags: "snz", ShortArgs: map[byte]types.OptionRole{'b': types.OptionValue, 'c': types.OptionValue, 'd': types.OptionValue, 'f': types.OptionValue}, Long: options.Merge(options.Longs(types.OptionNone, "only-delimited", "complement", "zero-terminated"), options.Longs(types.OptionValue, "bytes", "characters", "delimiter", "fields", "output-delimiter"))}

var diffSpec = types.OptionSpec{ShortFlags: "uNqsbwBiaytcpTdreEZ", ShortArgs: map[byte]types.OptionRole{'U': types.OptionValue, 'C': types.OptionValue, 'x': types.OptionValue, 'X': types.OptionRead, 'L': types.OptionValue, 'W': types.OptionValue, 'I': types.OptionValue, 'F': types.OptionValue, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "brief", "report-identical-files", "new-file", "unidirectional-new-file", "ignore-case", "ignore-space-change", "ignore-all-space", "ignore-blank-lines", "ignore-tab-expansion", "ignore-trailing-space", "text", "side-by-side", "expand-tabs", "initial-tab", "minimal", "speed-large-files", "strip-trailing-cr", "suppress-common-lines", "show-c-function", "left-column", "suppress-blank-empty", "no-dereference"), options.Longs(types.OptionRecursive, "recursive"), options.Longs(types.OptionOptionalValue, "unified", "context", "color", "normal"), options.Longs(types.OptionValue, "exclude", "label", "width", "ignore-matching-lines", "show-function-line", "starting-file", "horizon-lines", "tabsize", "palette", "line-format", "old-line-format", "new-line-format", "unchanged-line-format", "old-group-format", "new-group-format", "changed-group-format", "unchanged-group-format"), options.Longs(types.OptionRead, "exclude-from", "from-file", "to-file"))}

var duSpec = types.OptionSpec{ShortFlags: "achksxLHbmPl0", ShortArgs: map[byte]types.OptionRole{'d': types.OptionValue, 'B': types.OptionValue, 't': types.OptionValue, 'X': types.OptionRead},
	Long: options.Merge(options.Longs(types.OptionNone, "summarize", "human-readable", "all", "total", "apparent-size", "si", "bytes", "dereference", "one-file-system", "count-links", "null", "inodes"), options.Longs(types.OptionValue, "max-depth", "exclude", "threshold", "block-size"), options.Longs(types.OptionOptionalValue, "time"), options.Longs(types.OptionRead, "exclude-from", "files0-from"))}

var fileSpec = types.OptionSpec{ShortFlags: "bhiLszkNrp0", ShortArgs: map[byte]types.OptionRole{'m': types.OptionRead, 'f': types.OptionRead, 'F': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "brief", "mime", "mime-type", "mime-encoding", "dereference", "no-dereference", "special-files", "uncompress", "keep-going"), options.Longs(types.OptionRead, "files-from", "magic-file"), options.Longs(types.OptionValue, "separator"))}

var grepSpec = types.OptionSpec{ShortFlags: "abcEFGhHiIlLnoqrRsUvwxyzZP", ShortArgs: map[byte]types.OptionRole{'e': types.OptionValue, 'f': types.OptionRead, 'm': types.OptionValue, 'A': types.OptionValue, 'B': types.OptionValue, 'C': types.OptionValue, 'd': types.OptionValue, 'D': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "count", "files-with-matches", "files-without-match", "word-regexp", "line-regexp", "fixed-strings", "extended-regexp", "perl-regexp", "basic-regexp", "invert-match", "only-matching", "no-filename", "with-filename", "quiet", "silent", "no-messages", "text", "null", "null-data", "line-number", "ignore-case", "no-ignore-case", "byte-offset", "initial-tab", "line-buffered"), options.Longs(types.OptionRecursive, "recursive", "dereference-recursive"), options.Longs(types.OptionOptionalValue, "color", "colour"), options.Longs(types.OptionValue, "include", "exclude", "exclude-dir", "binary-files", "max-count", "after-context", "before-context", "context", "regexp", "label", "devices", "directories"), options.Longs(types.OptionRead, "file", "exclude-from"))}

var headSpec = types.OptionSpec{ShortFlags: "qvz", ShortArgs: map[byte]types.OptionRole{'n': types.OptionValue, 'c': types.OptionValue}, NumericShort: true,
	Long: options.Merge(options.Longs(types.OptionNone, "quiet", "silent", "verbose", "zero-terminated"), options.Longs(types.OptionValue, "lines", "bytes"))}

var lsSpec = types.OptionSpec{ShortFlags: "aAbBcCdDfFghHiklLmNnopqQrRsStuUvxXZ1", ShortArgs: map[byte]types.OptionRole{'I': types.OptionValue, 'w': types.OptionValue, 'T': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "all", "almost-all", "human-readable", "recursive", "reverse", "classify", "directory", "group-directories-first", "inode", "size", "full-time", "numeric-uid-gid", "dereference", "si", "literal", "quote-name", "escape", "hide-control-chars", "show-control-chars", "no-group", "author"), options.Longs(types.OptionOptionalValue, "color", "classify", "hyperlink", "indicator-style"), options.Longs(types.OptionValue, "sort", "time", "format", "time-style", "ignore", "hide", "width", "tabsize", "block-size", "quoting-style"))}

var readlinkSpec = types.OptionSpec{ShortFlags: "efmnqsvz", Long: options.Longs(types.OptionNone, "canonicalize", "canonicalize-existing", "canonicalize-missing", "no-newline", "quiet", "silent", "verbose", "zero")}

var realpathSpec = types.OptionSpec{ShortFlags: "emLPqsz", Long: options.Merge(options.Longs(types.OptionNone, "canonicalize-existing", "canonicalize-missing", "logical", "physical", "quiet", "strip", "no-symlinks", "zero"), options.Longs(types.OptionReadDir, "relative-to", "relative-base"))}

var sortSpec = types.OptionSpec{ShortFlags: "bdfginruMhsVzcCmR", ShortArgs: map[byte]types.OptionRole{'k': types.OptionValue, 't': types.OptionValue, 'o': types.OptionWrite, 'S': types.OptionValue, 'T': types.OptionWriteDir},
	Long: options.Merge(options.Longs(types.OptionNone, "unique", "reverse", "numeric-sort", "general-numeric-sort", "human-numeric-sort", "version-sort", "month-sort", "ignore-case", "ignore-leading-blanks", "dictionary-order", "stable", "merge", "random-sort", "zero-terminated"), options.Longs(types.OptionOptionalValue, "check"), options.Longs(types.OptionValue, "key", "field-separator", "buffer-size", "parallel", "sort", "batch-size"), options.Longs(types.OptionWrite, "output"), options.Longs(types.OptionWriteDir, "temporary-directory"), options.Longs(types.OptionRead, "files0-from"), options.Longs(types.OptionExec, "compress-program"))}

var statSpec = types.OptionSpec{ShortFlags: "fLtx", ShortArgs: map[byte]types.OptionRole{'c': types.OptionValue}, Long: options.Merge(options.Longs(types.OptionNone, "dereference", "file-system", "terse"), options.Longs(types.OptionValue, "format", "printf"))}

var tailSpec = types.OptionSpec{ShortFlags: "qvfFz", ShortArgs: map[byte]types.OptionRole{'n': types.OptionValue, 'c': types.OptionValue, 's': types.OptionValue}, NumericShort: true,
	Long: options.Merge(options.Longs(types.OptionNone, "quiet", "silent", "verbose", "retry", "zero-terminated"), options.Longs(types.OptionOptionalValue, "follow"), options.Longs(types.OptionValue, "lines", "bytes", "pid", "sleep-interval", "max-unchanged-stats"))}

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
	case registry.CmdCat, registry.CmdHead, registry.CmdTail, registry.CmdFile, registry.CmdStat, registry.CmdReadlink, registry.CmdRealpath, registry.CmdCut, registry.CmdSort, registry.CmdDu, registry.CmdLs:
		if id == registry.CmdSort || id == registry.CmdCat || id == registry.CmdHead || id == registry.CmdTail || id == registry.CmdCut {
			recursive = false
		}
		if id == registry.CmdDu {
			recursive = true
		}
		for i, path := range p.Positional {
			add(path, false, recursive, p.PositionalRefs[i])
		}
		if len(p.Positional) == 0 && (id == registry.CmdLs || id == registry.CmdDu) {
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
	case registry.CmdDiff:
		for i, path := range p.Positional {
			add(path, false, recursive, p.PositionalRefs[i])
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
