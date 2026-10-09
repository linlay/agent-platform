package query

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/config"
)

const btwQuestionJSONPlaceholder = "{{question_json}}"

// BuildBTWUserMessage has no source prompt: without a configured template the
// message is only the question block.
func BuildBTWUserMessage(prompts config.BTWPromptsConfig, question string) string {
	template := strings.TrimSpace(prompts.UserPromptTemplate)
	questionJSON, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		questionJSON = []byte(`{"question":""}`)
	}
	if !strings.Contains(template, btwQuestionJSONPlaceholder) {
		return strings.TrimSpace(template + "\n\n<btw_question_json>\n" + string(questionJSON) + "\n</btw_question_json>")
	}
	return strings.ReplaceAll(template, btwQuestionJSONPlaceholder, string(questionJSON))
}

// btwFinalAnswerPrompt has no source default; empty falls back to the Run's
// ordinary step-limit prompt.
func btwFinalAnswerPrompt(prompts config.BTWPromptsConfig) string {
	return strings.TrimSpace(prompts.FinalAnswerPrompt)
}
