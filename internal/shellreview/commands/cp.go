package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

var cpSpec = types.OptionSpec{ShortFlags: "afinpPRrsTvuLHlxb", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "archive", "force", "interactive", "no-clobber", "recursive", "symbolic-link", "verbose", "no-target-directory", "parents", "dereference", "no-dereference", "link", "remove-destination", "strip-trailing-slashes", "one-file-system", "attributes-only"), options.Longs(types.OptionOptionalValue, "preserve", "update", "backup", "reflink", "sparse", "no-preserve"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

func cpOperands(args []string) effectDraft { return transferOperands(cpSpec, args, types.Create) }
