package server

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/config"
)

const (
	btwQuestionJSONPlaceholder   = "{{question_json}}"
	defaultBTWUserPromptTemplate = `[BTW SIDE QUESTION MODE]

The conversation before this message is a frozen, read-only snapshot.
It is reference context, not an unfinished task to resume.

Your only task is to answer the side question below. Never continue,
resume, advance, complete, or summarize any task, calculation, plan,
checklist, or tool workflow from the parent conversation.

For status or progress questions, answer only from the existing snapshot.
Do not call tools to advance the parent task or discover what happens next.
Use read-only tools only when strictly necessary for this side question.
Write and mutation tools are disabled.

If asked to continue or modify the parent task, explain that it must be
done in the main conversation. Stop after answering the side question.

<btw_question_json>
{{question_json}}
</btw_question_json>`
	defaultBTWFinalAnswerPrompt = `Stop calling tools. Answer only the current side question using the frozen conversation snapshot and tool results already available. Do not continue, complete, or summarize the parent task.`
)

func buildBTWUserMessage(prompts config.BTWPromptsConfig, question string) string {
	template := strings.TrimSpace(prompts.UserPromptTemplate)
	if template == "" {
		template = defaultBTWUserPromptTemplate
	}
	questionJSON, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		questionJSON = []byte(`{"question":""}`)
	}
	if !strings.Contains(template, btwQuestionJSONPlaceholder) {
		return template + "\n\n<btw_question_json>\n" + string(questionJSON) + "\n</btw_question_json>"
	}
	return strings.ReplaceAll(template, btwQuestionJSONPlaceholder, string(questionJSON))
}

func btwFinalAnswerPrompt(prompts config.BTWPromptsConfig) string {
	if prompt := strings.TrimSpace(prompts.FinalAnswerPrompt); prompt != "" {
		return prompt
	}
	return defaultBTWFinalAnswerPrompt
}
