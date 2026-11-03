package commands

import (
	"strconv"
	"strings"

	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
)

func simpleOperands(id registry.ID, args []string) effectDraft {
	if id == registry.CmdCd {
		var e effectDraft
		for i, p := range args {
			if !strings.HasPrefix(p, "-") {
				e.Files = append(e.Files, types.FileAccess{Path: p, Ref: types.ArgumentRef(i, p)})
			}
		}
		return e
	}
	// echo arguments are data after its small flag grammar; unknown spellings
	// are printed. printf can mutate shell variables via -v or the %n format.
	if id == registry.CmdEcho || id == registry.CmdTrue || id == registry.CmdFalse {
		return effectDraft{}
	}
	if id == registry.CmdPrintf {
		format := args
		if len(format) > 0 && format[0] == "--" {
			format = format[1:]
		}
		if len(format) > 0 && (strings.HasPrefix(format[0], "-") || printfChangesState(format[0])) {
			return unknownOperands(args)
		}
		return effectDraft{}
	}
	if id == registry.CmdTest {
		return testOperands(args)
	}
	// top only reads process state; its options are not modeled.
	if len(args) == 0 {
		return effectDraft{}
	}
	return unknownOperands(args)
}

// https://www.gnu.org/software/bash/manual/html_node/Bash-Builtins.html
// printf -v and %n assign shell variables; format data must not change scope.
func printfChangesState(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i < len(format) && format[i] == '%' {
			continue
		}
		for i < len(format) && strings.ContainsRune("-+ #0.123456789*", rune(format[i])) {
			i++
		}
		if i >= len(format) || format[i] == 'n' || !strings.ContainsRune("diouxXfFeEgGaAcsbqQ", rune(format[i])) {
			return true
		}
	}
	return false
}
func testOperands(args []string) effectDraft {
	e := effectDraft{}
	add := func(i int) {
		e.Files = append(e.Files, types.FileAccess{Path: args[i], Ref: types.ArgumentRef(i, args[i])})
	}
	if len(args) <= 1 {
		return e
	}
	if len(args) == 2 {
		switch args[0] {
		case "-e", "-f", "-d", "-L", "-h", "-r", "-w", "-x", "-s", "-b", "-c", "-p", "-S", "-O", "-G", "-N":
			add(1)
			return e
		case "-z", "-n":
			return e
		}
	}
	if len(args) == 3 {
		switch args[1] {
		case "-nt", "-ot", "-ef":
			add(0)
			add(2)
			return e
		case "=", "==", "!=":
			return e
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			_, left := strconv.ParseInt(args[0], 10, 64)
			_, right := strconv.ParseInt(args[2], 10, 64)
			if left == nil && right == nil {
				return e
			}
		}
	}
	return unknownOperands(args)
}
