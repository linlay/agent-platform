package conversationexport

import (
	"fmt"
	"strings"
)

func RenderMarkdown(snapshot SnapshotV1) ([]byte, error) {
	var out strings.Builder
	for _, turn := range snapshot.Turns {
		if turn.Outcome != OutcomeCompleted || len(turn.Nodes) == 0 || turn.Nodes[0].Kind != "message" || turn.Nodes[0].Role != "user" {
			continue
		}
		answer := ""
		for index := len(turn.Nodes) - 1; index > 0; index-- {
			if turn.Nodes[index].Kind == "content" && turn.Nodes[index].Role == "assistant" {
				answer = turn.Nodes[index].Text
				break
			}
		}
		if strings.TrimSpace(answer) == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n---\n\n")
		}
		out.WriteString("## 用户问题\n\n")
		out.WriteString(turn.Nodes[0].Text)
		for _, steer := range turn.Nodes[1:] {
			if steer.Kind != "message" || steer.Role != "user" || steer.SteerID == "" || strings.TrimSpace(steer.Text) == "" {
				continue
			}
			out.WriteString("\n\n### 补充指令\n\n")
			out.WriteString(steer.Text)
			if origin := steer.Origin; origin != nil {
				fmt.Fprintf(&out, "\n\n来源：Agent `%s`，Chat `%s`，Run `%s`，Tool `%s`。", origin.AgentKey, origin.ChatID, origin.RunID, origin.ToolID)
			}
		}
		out.WriteString("\n\n## 助手回答\n\n")
		out.WriteString(answer)
		out.WriteString("\n")
	}
	if out.Len() == 0 {
		return nil, ErrNoCompletedTurn
	}
	return []byte(out.String()), nil
}
