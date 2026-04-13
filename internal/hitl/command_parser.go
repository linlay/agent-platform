package hitl

import (
	"path/filepath"
	"strings"
	"unicode"
)

func ParseCommandComponents(command string) CommandComponents {
	tokens := splitShellLikeFirstSegment(command)
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

func splitMatchTokens(match string) []string {
	tokens := splitShellLikeFirstSegment(match)
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

func splitShellLikeFirstSegment(command string) []string {
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
			case r == '|':
				flush()
				return tokens
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
	flush()
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
