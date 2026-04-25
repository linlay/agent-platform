package hitl

import (
	"path/filepath"
	"strings"
	"unicode"

	"agent-platform-runner-go/internal/bashast"
)

func ParseCommandComponents(command string) CommandComponents {
	return parseCommandTokens(splitShellLikeFirstSegment(command))
}

func ParseCommandComponentsFromAST(cmds []bashast.SimpleCommand) []CommandComponents {
	components := make([]CommandComponents, 0, len(cmds))
	for _, cmd := range cmds {
		if len(cmd.Argv) == 0 {
			components = append(components, CommandComponents{})
			continue
		}
		base := strings.TrimSpace(filepath.Base(cmd.Argv[0]))
		if base == "" || base == "." || base == string(filepath.Separator) {
			components = append(components, CommandComponents{})
			continue
		}
		tokens := make([]string, 0, len(cmd.Argv)-1)
		for _, token := range cmd.Argv[1:] {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			tokens = append(tokens, token)
		}
		components = append(components, CommandComponents{BaseCommand: base, Tokens: tokens})
	}
	return components
}

func splitMatchTokens(match string) []string {
	trimmed := strings.TrimSpace(match)
	if strings.HasPrefix(trimmed, "|") {
		tokens := splitShellLikeTokens(strings.TrimSpace(strings.TrimPrefix(trimmed, "|")))
		out := make([]string, 0, len(tokens)+1)
		out = append(out, "|")
		for _, token := range tokens {
			token = strings.ToLower(strings.TrimSpace(token))
			if token == "" {
				continue
			}
			out = append(out, token)
		}
		return out
	}
	tokens := splitShellLikeTokens(match)
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		out = append(out, token)
	}
	return out
}

func parseCommandTokens(tokens []string) CommandComponents {
	if len(tokens) == 0 {
		return CommandComponents{}
	}

	baseIndex := -1
	for idx, token := range tokens {
		if isEnvAssignment(token) {
			continue
		}
		baseIndex = idx
		break
	}
	if baseIndex < 0 {
		return CommandComponents{}
	}

	base := strings.TrimSpace(filepath.Base(tokens[baseIndex]))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return CommandComponents{}
	}

	args := make([]string, 0, len(tokens)-(baseIndex+1))
	for _, token := range tokens[baseIndex+1:] {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		args = append(args, token)
	}

	return CommandComponents{
		BaseCommand: base,
		Tokens:      args,
	}
}

func splitShellLikeFirstSegment(command string) []string {
	segments := splitShellLikeSegments(command)
	if len(segments) == 0 {
		return nil
	}
	return splitShellLikeTokens(segments[0])
}

func splitShellLikeSegments(command string) []string {
	var (
		segments []string
		current  strings.Builder
		quote    rune
		escaped  bool
	)

	flushSegment := func() {
		segment := strings.TrimSpace(current.String())
		current.Reset()
		if segment == "" {
			return
		}
		segments = append(segments, segment)
	}

	runes := []rune(command)
	for idx := 0; idx < len(runes); idx++ {
		r := runes[idx]
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			current.WriteRune(r)
			if r == '\'' {
				quote = 0
			}
		case quote == '"':
			current.WriteRune(r)
			if r == '"' {
				quote = 0
				continue
			}
			if r == '\\' {
				escaped = true
			}
		default:
			switch r {
			case '\'', '"':
				quote = r
				current.WriteRune(r)
			case '\\':
				escaped = true
				current.WriteRune(r)
			case ';':
				flushSegment()
			case '|', '&':
				flushSegment()
				if idx+1 < len(runes) && runes[idx+1] == r {
					idx++
				}
			default:
				current.WriteRune(r)
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flushSegment()
	return segments
}

func splitShellLikePipelineSegments(command string) []string {
	var (
		segments []string
		current  strings.Builder
		quote    rune
		escaped  bool
	)

	flushSegment := func() {
		segment := strings.TrimSpace(current.String())
		current.Reset()
		if segment == "" {
			return
		}
		segments = append(segments, segment)
	}

	runes := []rune(command)
	for idx := 0; idx < len(runes); idx++ {
		r := runes[idx]
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			current.WriteRune(r)
			if r == '\'' {
				quote = 0
			}
		case quote == '"':
			current.WriteRune(r)
			if r == '"' {
				quote = 0
				continue
			}
			if r == '\\' {
				escaped = true
			}
		default:
			switch r {
			case '\'', '"':
				quote = r
				current.WriteRune(r)
			case '\\':
				escaped = true
				current.WriteRune(r)
			case '|':
				if idx+1 < len(runes) && runes[idx+1] == '|' {
					current.WriteRune(r)
					current.WriteRune(runes[idx+1])
					idx++
					continue
				}
				flushSegment()
			default:
				current.WriteRune(r)
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flushSegment()
	return segments
}

func splitShellLikeTokens(command string) []string {
	var (
		tokens  []string
		current strings.Builder
		quote   rune
		escaped bool
	)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}
	flushSegment := func() {
		flush()
	}

	for _, r := range command {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case quote == '"':
			if r == '"' {
				quote = 0
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			current.WriteRune(r)
		default:
			switch {
			case unicode.IsSpace(r):
				flush()
			case r == '\'' || r == '"':
				quote = r
			case r == '\\':
				escaped = true
			default:
				current.WriteRune(r)
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flushSegment()
	return tokens
}

func isEnvAssignment(token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	idx := strings.IndexRune(token, '=')
	if idx <= 0 || idx == len(token)-1 {
		return false
	}
	key := token[:idx]
	for pos, r := range key {
		if pos == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
