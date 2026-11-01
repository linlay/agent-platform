package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

func awkOperands(args []string) types.Effects {
	var e types.Effects
	var positional []string
	program := ""
	hasProgram := false
	end := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if end || a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		switch {
		case a == "--":
			end = true
		case a == "-F" || a == "-v":
			i++
			if i >= len(args) {
				e.Incomplete = true
			}
		case strings.HasPrefix(a, "-F") || strings.HasPrefix(a, "-v"):
		case a == "-f" || a == "--file" || a == "-i" || a == "--include" || a == "-l" || a == "--load" || a == "-e" || a == "--source":
			// Program files, includes and extensions are not frozen by this analysis.
			e.ExecutesCode = true
			if i+1 < len(args) {
				i++
				if a == "-f" || a == "--file" || a == "-i" || a == "--include" {
					e.Files = append(e.Files, types.FileAccess{Path: args[i]})
				}
				hasProgram = hasProgram || a == "-f" || a == "--file" || a == "-e" || a == "--source"
			}
		default:
			e.Incomplete = true
		}
	}
	if !hasProgram && len(positional) > 0 {
		program, positional, hasProgram = positional[0], positional[1:], true
		if !awkProgramSafe(program) {
			e.ExecutesCode = true
		}
	}
	for _, p := range positional {
		if name, _, ok := strings.Cut(p, "="); ok && isIdentifier(name) {
			continue // command-line variable assignment
		}
		if p != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: p})
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

// awkProgramSafe rejects programs that can run commands, read other files or
// redirect output. Comparison operators are indistinguishable from output
// redirection without a full parser, so print statements with '>' are rejected.
func awkProgramSafe(program string) bool {
	var b strings.Builder
	prev := byte('(')
	for i := 0; i < len(program); i++ {
		c := program[i]
		switch {
		case c == '"':
			i++
			for i < len(program) && program[i] != '"' {
				if program[i] == '\\' {
					i++
				}
				i++
			}
			b.WriteString(`""`)
			prev = '"'
			continue
		case c == '/' && strings.IndexByte("(,~!{};&|\n", prev) >= 0:
			i++
			for i < len(program) && program[i] != '/' {
				if program[i] == '\\' {
					i++
				}
				if i < len(program) && program[i] == '\n' {
					return false
				}
				i++
			}
			b.WriteString("//")
			prev = '/'
			continue
		}
		b.WriteByte(c)
		if c != ' ' && c != '\t' {
			prev = c
		}
	}
	code := b.String()
	for _, word := range []string{"system", "getline", "@load", "@include", "|&"} {
		if strings.Contains(code, word) {
			return false
		}
	}
	if strings.Contains(strings.ReplaceAll(code, "||", ""), "|") {
		return false
	}
	if strings.Contains(code, "print") && strings.Contains(code, ">") {
		return false
	}
	return true
}

func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
