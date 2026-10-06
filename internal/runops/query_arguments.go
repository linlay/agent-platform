package runops

import (
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
)

func parseQueryArguments(args map[string]any) (contracts.RunStartRequest, error) {
	request := contracts.RunStartRequest{}
	invalid := func(message string) (contracts.RunStartRequest, error) {
		return request, &contracts.RunToolError{Code: "invalid_request", Message: message}
	}
	for key, value := range args {
		switch key {
		case "message", "agentKey", "teamId", "chatId", "chatName", "accessLevel":
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return invalid(key + " must be a non-empty string")
			}
			text = strings.TrimSpace(text)
			switch key {
			case "chatName":
				request.ChatName = text
			case "accessLevel":
				level, ok := contracts.NormalizeAccessLevel(text)
				if !ok {
					return invalid("accessLevel must be default, auto_approve, or full_access")
				}
				request.AccessLevel = level
			}
		case "mustUseSkills":
			var items []any
			switch values := value.(type) {
			case []any:
				items = values
			case []string:
				for _, id := range values {
					items = append(items, id)
				}
			default:
				return invalid("mustUseSkills must be an array of non-empty skill IDs")
			}
			for _, item := range items {
				id, ok := item.(string)
				if !ok || strings.TrimSpace(id) == "" {
					return invalid("mustUseSkills must contain non-empty strings")
				}
				request.MustUseSkills = append(request.MustUseSkills, strings.TrimSpace(id))
			}
		default:
			message := fmt.Sprintf("unknown chat_start argument %q", key)
			if key == "taskName" {
				message += "; use chatName for a new Chat"
			}
			return request, &contracts.RunToolError{Code: "unknown_argument", Message: message}
		}
	}
	if request.ChatName != "" {
		if _, exists := args["chatId"]; exists {
			return invalid("chatName cannot be combined with chatId")
		}
	}
	return request, nil
}
