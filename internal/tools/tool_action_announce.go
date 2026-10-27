package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
)

// actionAnnounceMaxRunes matches the maxLength in action_announce.yml.
const actionAnnounceMaxRunes = 120

// invokeActionAnnounce only validates: the announcement itself was published as
// the progress line once the arguments were complete.
func invokeActionAnnounce(args map[string]any) ToolExecutionResult {
	message := strings.TrimSpace(AnyStringNode(args["message"]))
	if message == "" {
		return ToolExecutionResult{Output: "失败: 缺少 message", Error: "missing_message", ExitCode: -1}
	}
	if utf8.RuneCountInString(message) > actionAnnounceMaxRunes {
		return ToolExecutionResult{Output: fmt.Sprintf("失败: message 超过 %d 个字符，请改成一句更短的话", actionAnnounceMaxRunes), Error: "message_too_long", ExitCode: -1}
	}
	return ToolExecutionResult{Output: "announced", ExitCode: 0}
}
