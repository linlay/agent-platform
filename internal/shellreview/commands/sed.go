package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

func sedOperands(args []string) types.Effects {
	var e types.Effects
	var scripts, positional []string
	inPlace, sandbox, fromFile := false, false, false
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
		case a == "-e" || a == "--expression":
			if i+1 >= len(args) {
				e.Incomplete = true
				continue
			}
			i++
			scripts = append(scripts, args[i])
		case strings.HasPrefix(a, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(a, "--expression="))
		case strings.HasPrefix(a, "-e") && len(a) > 2:
			scripts = append(scripts, a[2:])
		case a == "-f" || a == "--file":
			if i+1 >= len(args) {
				e.Incomplete = true
				continue
			}
			i++
			fromFile = true
			e.Files = append(e.Files, types.FileAccess{Path: args[i]})
		case strings.HasPrefix(a, "--file="):
			fromFile = true
			e.Files = append(e.Files, types.FileAccess{Path: strings.TrimPrefix(a, "--file=")})
		case a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place="):
			inPlace = true
		case a == "-l" || a == "--line-length":
			i++
		case a == "--sandbox":
			sandbox = true
		case a == "--posix" || a == "--debug" || a == "--quiet" || a == "--silent" || a == "--regexp-extended" || a == "--separate" || a == "--unbuffered" || a == "--null-data" || a == "--follow-symlinks" || strings.HasPrefix(a, "--line-length="):
		case strings.Trim(a[1:], "nErsuz") == "":
		default:
			e.Incomplete = true
		}
	}
	if len(scripts) == 0 && !fromFile && len(positional) > 0 {
		scripts = append(scripts, positional[0])
		positional = positional[1:]
	}
	if fromFile {
		// A script file is not frozen by this analysis.
		e.ExecutesCode = true
	}
	for _, script := range scripts {
		if !sandbox && !sedScriptSafe(script) {
			e.ExecutesCode = true
		}
	}
	for _, p := range positional {
		if p != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: inPlace})
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

// sedScriptSafe accepts scripts made only of commands that transform or print
// the stream. e, w/W, r/R and the s///e and s///w flags run programs or touch
// files and make the script unsafe; anything not understood is unsafe.
func sedScriptSafe(script string) bool {
	s := script
	i := 0
	depth := 0
	skipSpace := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
	}
	readDelimited := func(delim byte) bool {
		for i < len(s) {
			c := s[i]
			if c == '\\' {
				i += 2
				continue
			}
			if c == '\n' && delim != '\n' {
				return false
			}
			i++
			if c == delim {
				return true
			}
		}
		return false
	}
	readAddress := func() bool {
		switch {
		case i < len(s) && s[i] >= '0' && s[i] <= '9':
			for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '~') {
				i++
			}
		case i < len(s) && s[i] == '$':
			i++
		case i < len(s) && s[i] == '/':
			i++
			if !readDelimited('/') {
				return false
			}
			for i < len(s) && (s[i] == 'I' || s[i] == 'M') {
				i++
			}
		case i+1 < len(s) && s[i] == '\\':
			delim := s[i+1]
			i += 2
			if !readDelimited(delim) {
				return false
			}
			for i < len(s) && (s[i] == 'I' || s[i] == 'M') {
				i++
			}
		}
		return true
	}
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' {
			i++
			continue
		}
		if c == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if !readAddress() {
			return false
		}
		skipSpace()
		if i < len(s) && s[i] == ',' {
			i++
			skipSpace()
			if i < len(s) && (s[i] == '+' || s[i] == '~') {
				i++
				for i < len(s) && s[i] >= '0' && s[i] <= '9' {
					i++
				}
			} else if !readAddress() {
				return false
			}
		}
		skipSpace()
		for i < len(s) && s[i] == '!' {
			i++
			skipSpace()
		}
		if i >= len(s) {
			return false
		}
		cmd := s[i]
		i++
		switch cmd {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		case '=', 'd', 'D', 'g', 'G', 'h', 'H', 'n', 'N', 'p', 'P', 'x', 'z', 'F':
		case 'l', 'q', 'Q', 'L':
			skipSpace()
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		case 'b', 't', 'T', ':':
			for i < len(s) && s[i] != '\n' && s[i] != ';' && s[i] != '}' {
				i++
			}
		case 'a', 'i', 'c':
			// Text runs to the end of the line; a trailing backslash continues it.
			for i < len(s) && s[i] != '\n' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		case 'y':
			if i >= len(s) {
				return false
			}
			delim := s[i]
			i++
			if !readDelimited(delim) || !readDelimited(delim) {
				return false
			}
		case 's':
			if i >= len(s) || s[i] == '\n' || s[i] == '\\' {
				return false
			}
			delim := s[i]
			i++
			if !readDelimited(delim) || !readDelimited(delim) {
				return false
			}
			for i < len(s) {
				f := s[i]
				if f == 'g' || f == 'p' || f == 'i' || f == 'I' || f == 'm' || f == 'M' || f >= '0' && f <= '9' {
					i++
					continue
				}
				if f == 'e' || f == 'w' || f == 'W' {
					return false
				}
				break
			}
		default:
			// e, w, W, r, R and unknown commands.
			return false
		}
	}
	return depth == 0
}
