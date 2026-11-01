package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

var lnSpec = types.OptionSpec{ShortFlags: "sfnvTbrLPi", ShortArgs: map[byte]types.OptionRole{'t': types.OptionWriteDir, 'S': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "symbolic", "force", "no-dereference", "verbose", "relative", "logical", "physical", "interactive", "no-target-directory"), options.Longs(types.OptionOptionalValue, "backup"), options.Longs(types.OptionValue, "suffix"), options.Longs(types.OptionWriteDir, "target-directory"))}

func lnOperands(args []string) effectDraft { return transferOperands(lnSpec, args, types.Link) }
