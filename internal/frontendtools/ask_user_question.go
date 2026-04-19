package frontendtools

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/stream"
)

type AskUserQuestionHandler struct{}

func NewAskUserQuestionHandler() *AskUserQuestionHandler {
	return &AskUserQuestionHandler{}
}

func (h *AskUserQuestionHandler) ToolName() string {
	return "_ask_user_question_"
}

func (h *AskUserQuestionHandler) ValidateArgs(args map[string]any) error {
	if !strings.EqualFold(strings.TrimSpace(contracts.AnyStringNode(args["mode"])), "question") {
		return fmt.Errorf("ask_user_question mode must be question")
	}

	rawQuestions, ok := args["questions"].([]any)
	if !ok || len(rawQuestions) == 0 {
		return fmt.Errorf("ask_user_question questions must be a non-empty array")
	}

	for index, rawQuestion := range rawQuestions {
		question := contracts.AnyMapNode(rawQuestion)
		if len(question) == 0 {
			return fmt.Errorf("question %d must be an object", index+1)
		}
		questionText := strings.TrimSpace(contracts.AnyStringNode(question["question"]))
		if questionText == "" {
			return fmt.Errorf("question %d: question is required", index+1)
		}
		if _, hasLegacyField := question["multiSelect"]; hasLegacyField {
			return fmt.Errorf("%s: multiSelect is no longer supported; use multiple", questionText)
		}
		questionType := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(question["type"])))
		if questionType == "" {
			return fmt.Errorf("%s: type is required", questionText)
		}
		if questionType != "select" {
			continue
		}
		options, ok := question["options"].([]any)
		if !ok || len(options) == 0 {
			return fmt.Errorf("%s: options is required for select questions", questionText)
		}
		for optionIndex, rawOption := range options {
			option := contracts.AnyMapNode(rawOption)
			label := strings.TrimSpace(contracts.AnyStringNode(option["label"]))
			if label == "" {
				return fmt.Errorf("%s: option %d label is required", questionText, optionIndex+1)
			}
		}
	}
	return nil
}

func (h *AskUserQuestionHandler) BuildInitialAwaitAsk(toolID string, runID string, _ api.ToolDetailResponse, args map[string]any, chunkIndex int, timeoutMs int64) *stream.AwaitAsk {
	if chunkIndex != 0 {
		return nil
	}
	questions := buildAwaitQuestions(args)
	if len(questions) == 0 {
		return nil
	}
	return &stream.AwaitAsk{
		AwaitingID: toolID,
		Mode:       "question",
		Timeout:    timeoutMs,
		RunID:      runID,
		Questions:  questions,
	}
}

func (h *AskUserQuestionHandler) NormalizeSubmit(args map[string]any, params any) (map[string]any, error) {
	rawAnswers, err := decodeSubmitItems(params)
	if err != nil {
		return nil, fmt.Errorf("ask_user_question submit params must be an array")
	}
	if len(rawAnswers) == 0 {
		return map[string]any{
			"mode":      "question",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
	}

	questionDefs := questionDefinitionsInOrder(args)
	if len(rawAnswers) != len(questionDefs) {
		return nil, fmt.Errorf("expected %d answers, got %d", len(questionDefs), len(rawAnswers))
	}

	answers := make([]map[string]any, 0, len(rawAnswers))
	for index, answerMap := range rawAnswers {
		definition := questionDefs[index]
		questionText := contracts.AnyStringNode(definition["question"])
		rawValue, err := normalizeQuestionSubmitValue(definition, answerMap)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", questionText, err)
		}
		normalizedAnswer, err := normalizeQuestionAnswer(definition, rawValue)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", questionText, err)
		}
		answers = append(answers, map[string]any{
			"id":       contracts.AnyStringNode(definition["id"]),
			"question": questionText,
			"header":   contracts.AnyStringNode(definition["header"]),
			"answer":   normalizedAnswer,
		})
	}

	return map[string]any{
		"mode":    "question",
		"answers": answers,
	}, nil
}

func (h *AskUserQuestionHandler) FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "summary":
		return formatQuestionSummary(result), true
	case "kv":
		return formatQuestionKV(result), true
	case "qa":
		return formatQuestionQA(result), true
	default:
		return "", false
	}
}

func buildAwaitQuestions(args map[string]any) []any {
	rawQuestions := asAnySlice(args["questions"])
	if len(rawQuestions) == 0 {
		return nil
	}
	questions := make([]any, 0, len(rawQuestions))
	for index, rawQuestion := range rawQuestions {
		question, ok := deepCloneAny(rawQuestion).(map[string]any)
		if !ok || len(question) == 0 {
			continue
		}
		question["id"] = questionDefinitionID(question, index)
		questions = append(questions, sanitizeQuestionFields([]any{question})[0])
	}
	return questions
}

func questionDefinitionsInOrder(args map[string]any) []map[string]any {
	rawQuestions := asAnySlice(args["questions"])
	definitions := make([]map[string]any, 0, len(rawQuestions))
	for index, rawQuestion := range rawQuestions {
		question, ok := deepCloneAny(rawQuestion).(map[string]any)
		if !ok || len(question) == 0 {
			continue
		}
		id := questionDefinitionID(question, index)
		question["id"] = id
		definitions = append(definitions, question)
	}
	return definitions
}

func questionDefinitionID(question map[string]any, index int) string {
	if id := strings.TrimSpace(contracts.AnyStringNode(question["id"])); id != "" {
		return id
	}
	return fmt.Sprintf("q%d", index+1)
}

func decodeSubmitItems(params any) ([]map[string]any, error) {
	switch typed := params.(type) {
	case api.SubmitParams:
		return api.DecodeSubmitParams(typed)
	case []json.RawMessage:
		return api.DecodeSubmitParams(api.SubmitParams(typed))
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			item, ok := raw.(map[string]any)
			if !ok || len(item) == 0 {
				return nil, fmt.Errorf("submit items must be objects")
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("submit params must be an array")
	}
}

func normalizeQuestionSubmitValue(definition map[string]any, answerMap map[string]any) (any, error) {
	questionType := strings.ToLower(contracts.AnyStringNode(definition["type"]))
	_, hasAnswer := answerMap["answer"]
	rawAnswers, hasAnswers := answerMap["answers"]

	if questionType == "select" && contracts.AnyBoolNode(definition["multiple"]) {
		if hasAnswer && hasAnswers {
			return nil, fmt.Errorf("answer and answers cannot both be provided")
		}
		if !hasAnswers {
			return nil, fmt.Errorf("answers is required for multiple questions")
		}
		return rawAnswers, nil
	}

	if hasAnswers {
		return nil, fmt.Errorf("answers is only allowed for multiple questions")
	}
	if !hasAnswer {
		return nil, fmt.Errorf("answer is required")
	}
	return answerMap["answer"], nil
}

func formatQuestionSummary(result contracts.ToolExecutionResult) string {
	answers, ok := structuredAnswers(result)
	if !ok || len(answers) == 0 {
		return result.Output
	}
	lines := make([]string, 0, len(answers)+1)
	lines = append(lines, "用户回答了以下问题:")
	for _, answer := range answers {
		key := formatAnswerKey(answer)
		if key == "" {
			return result.Output
		}
		lines = append(lines, "- "+key+": "+formatAnswerValue(answer["answer"]))
	}
	return strings.Join(lines, "\n")
}

func formatQuestionKV(result contracts.ToolExecutionResult) string {
	answers, ok := structuredAnswers(result)
	if !ok || len(answers) == 0 {
		return result.Output
	}
	items := make([]string, 0, len(answers))
	for _, answer := range answers {
		key := formatAnswerKey(answer)
		if key == "" {
			return result.Output
		}
		items = append(items, key+"="+formatAnswerValue(answer["answer"]))
	}
	return strings.Join(items, "; ")
}

func formatQuestionQA(result contracts.ToolExecutionResult) string {
	answers, ok := structuredAnswers(result)
	if !ok || len(answers) == 0 {
		return result.Output
	}
	lines := make([]string, 0, len(answers)*2)
	for _, answer := range answers {
		question := strings.TrimSpace(contracts.AnyStringNode(answer["question"]))
		if question == "" {
			question = strings.TrimSpace(contracts.AnyStringNode(answer["header"]))
		}
		if question == "" {
			return result.Output
		}
		lines = append(lines, "问题："+question)
		lines = append(lines, "回答："+formatAnswerValue(answer["answer"]))
	}
	return strings.Join(lines, "\n")
}
