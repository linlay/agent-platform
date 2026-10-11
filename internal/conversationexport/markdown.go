package conversationexport

import (
	"fmt"
	"strings"
)

func RenderMarkdown(snapshot SnapshotV1) ([]byte, error) {
	selected := make([]struct {
		question string
		answer   string
		steers   []NodeV1
	}, 0, len(snapshot.Turns))
	totalBytes := 0
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
		question := turn.Nodes[0].Text
		var steers []NodeV1
		for _, node := range turn.Nodes[1:] {
			if node.Kind == "message" && node.Role == "user" && node.SteerID != "" && strings.TrimSpace(node.Text) != "" {
				steers = append(steers, node)
			}
		}
		selected = append(selected, struct {
			question string
			answer   string
			steers   []NodeV1
		}{question: question, answer: answer, steers: steers})
		totalBytes += len(question) + len(answer) + 64
	}
	if len(selected) == 0 {
		return nil, ErrNoCompletedTurn
	}

	var out strings.Builder
	out.Grow(totalBytes)
	for index, turn := range selected {
		if index > 0 {
			out.WriteString("---\n\n")
		}
		out.WriteString("## 用户问题\n\n")
		out.WriteString(turn.question)
		for _, steer := range turn.steers {
			out.WriteString("\n\n### 补充指令\n\n")
			out.WriteString(steer.Text)
			if source := steer.Source; source != nil {
				fmt.Fprintf(&out, "\n\n来源：Agent `%s`，Chat `%s`，Run `%s`，Tool `%s`。", source.AgentKey, source.ChatID, source.RunID, source.ToolID)
			}
		}
		out.WriteString("\n\n## 助手回答\n\n")
		out.WriteString(turn.answer)
		out.WriteString("\n")
		if index < len(selected)-1 {
			out.WriteString("\n")
		}
	}
	return []byte(out.String()), nil
}
