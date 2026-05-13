package llm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"agent-platform-runner-go/internal/hitl"
)

func extractCommandPayload(parsed hitl.CommandComponents) map[string]any {
	for idx := 0; idx < len(parsed.Tokens)-1; idx++ {
		if strings.TrimSpace(parsed.Tokens[idx]) != "--payload" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(parsed.Tokens[idx+1]), &payload); err != nil {
			return nil
		}
		if payload == nil {
			return nil
		}
		return payload
	}
	return nil
}

func extractPayloadFromOriginalCommand(command string) map[string]any {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	tokenSpans := firstSegmentTokenSpans(command)
	for idx := 0; idx < len(tokenSpans)-1; idx++ {
		if strings.TrimSpace(tokenSpans[idx].Text) != "--payload" {
			continue
		}
		rawToken := strings.TrimSpace(command[tokenSpans[idx+1].Start:tokenSpans[idx+1].End])
		if rawToken == "" {
			return nil
		}
		if unquoted, ok := shellUnquotePayloadToken(rawToken); ok {
			rawToken = unquoted
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(rawToken), &payload); err != nil {
			return nil
		}
		if payload == nil {
			return nil
		}
		return payload
	}
	return nil
}

func shellUnquotePayloadToken(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if len(token) < 2 {
		return token, false
	}
	switch token[0] {
	case '\'':
		if token[len(token)-1] != '\'' {
			return token, false
		}
		return token[1 : len(token)-1], true
	case '"':
		unquoted, err := strconv.Unquote(token)
		if err == nil {
			return unquoted, true
		}
		if token[len(token)-1] != '"' {
			return token, false
		}
		return token[1 : len(token)-1], true
	default:
		return token, false
	}
}

func reconstructCommandWithPayload(command string, payload map[string]any) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("original command is required")
	}
	if payload == nil {
		return "", fmt.Errorf("payload must be an object")
	}

	tokenSpans := firstSegmentTokenSpans(command)
	for idx := 0; idx < len(tokenSpans)-1; idx++ {
		if strings.TrimSpace(tokenSpans[idx].Text) != "--payload" {
			continue
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("marshal payload: %w", err)
		}
		replacement := shellQuoteToken(string(payloadJSON))
		valueSpan := tokenSpans[idx+1]
		return command[:valueSpan.Start] + replacement + command[valueSpan.End:], nil
	}
	return "", fmt.Errorf("original command does not contain --payload")
}

type shellTokenSpan struct {
	Start int
	End   int
	Text  string
}

func firstSegmentTokenSpans(command string) []shellTokenSpan {
	var (
		spans      []shellTokenSpan
		current    strings.Builder
		tokenStart = -1
		quote      rune
		escaped    bool
	)

	flush := func(end int) {
		if tokenStart < 0 || current.Len() == 0 {
			tokenStart = -1
			current.Reset()
			return
		}
		spans = append(spans, shellTokenSpan{
			Start: tokenStart,
			End:   end,
			Text:  current.String(),
		})
		tokenStart = -1
		current.Reset()
	}

	for idx, r := range command {
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
			case r == '|':
				flush(idx)
				return spans
			case r == '\'' || r == '"':
				if tokenStart < 0 {
					tokenStart = idx
				}
				quote = r
			case r == '\\':
				if tokenStart < 0 {
					tokenStart = idx
				}
				escaped = true
			case strings.ContainsRune(" \t\r\n", r):
				flush(idx)
			default:
				if tokenStart < 0 {
					tokenStart = idx
				}
				current.WriteRune(r)
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush(len(command))
	return spans
}

func shellQuoteToken(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
