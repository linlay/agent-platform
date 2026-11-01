package commands

import (
	"agent-platform/internal/shellreview/types"
	"strings"
)

func pdftotextOperands(args []string) types.Effects {
	var e types.Effects
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-f", "-l", "-r", "-x", "-y", "-W", "-H", "-enc", "-eol", "-opw", "-upw", "-fixed", "-cropbox":
			i++
			continue
		case "-layout", "-raw", "-nopgbrk", "-q", "-htmlmeta", "-bbox", "-bbox-layout", "-nodiag", "-listenc", "-v", "-tsv", "-simple", "-simple2":
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			e.Incomplete = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) > 0 {
		e.Files = append(e.Files, types.FileAccess{Path: positional[0]})
	}
	if len(positional) > 1 && positional[1] != "-" {
		e.Files = append(e.Files, types.FileAccess{Path: positional[1], Write: true})
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

var pdftotextSpec = types.OptionSpec{ShortFlags: "", ShortArgs: map[byte]types.OptionRole{}, Long: map[string]types.OptionRole{}}
