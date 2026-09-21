package conversationexport

import "strings"

func RenderMarkdown(snapshot SnapshotV1) ([]byte, error) {
	selected := make([]struct {
		question string
		answer   string
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
		selected = append(selected, struct {
			question string
			answer   string
		}{question: question, answer: answer})
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
		out.WriteString("\n\n## 助手回答\n\n")
		out.WriteString(turn.answer)
		out.WriteString("\n")
		if index < len(selected)-1 {
			out.WriteString("\n")
		}
	}
	return []byte(out.String()), nil
}
