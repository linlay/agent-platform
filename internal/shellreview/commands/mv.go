package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

var mvSpec = types.OptionSpec{ShortFlags: "finvuTb", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "force", "interactive", "no-clobber", "verbose", "no-target-directory", "strip-trailing-slashes"), options.Longs(types.OptionOptionalValue, "update", "backup"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

func mvOperands(args []string) effectDraft { return transferOperands(mvSpec, args, types.Rename) }
