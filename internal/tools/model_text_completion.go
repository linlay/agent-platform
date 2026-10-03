package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/modelrequest"
	"agent-platform/internal/models"
)

type textModelRequest struct {
	SystemPrompt    string
	UserPrompt      string
	MaxOutputTokens int
}

// textModelReasoningHeadroomTokens is added to the wire output limit of
// reasoning models, where reasoning and answer share one budget. Without it the
// reasoning alone can exhaust MaxOutputTokens and leave no answer.
const textModelReasoningHeadroomTokens = 4096

// errTextModelOutputTruncated marks a response cut off by the output token
// limit. Completion helpers return it together with whatever answer text was
// produced: empty text means the limit was hit before any answer, non-empty
// text is a usable but incomplete answer.
var errTextModelOutputTruncated = errors.New("model output truncated")

func textModelTruncatedError(text string, reason string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: output token limit reached before any answer text (finish reason: %s)", errTextModelOutputTruncated, reason)
	}
	return fmt.Errorf("%w: output token limit reached mid-answer (finish reason: %s)", errTextModelOutputTruncated, reason)
}

// textModelOutputLimit returns the wire output limit for an answer budget.
func textModelOutputLimit(model models.ModelDefinition, answerTokens int) int {
	if model.IsReasoner && answerTokens > 0 {
		return answerTokens + textModelReasoningHeadroomTokens
	}
	return answerTokens
}

const textModelTruncatedNotice = "\n\n[Output truncated: the model reached its output limit before finishing.]"

func textModelEmptyContentError(reason string) error {
	if reason = strings.TrimSpace(reason); reason != "" {
		return fmt.Errorf("model returned empty content (finish reason: %s)", reason)
	}
	return fmt.Errorf("model returned empty content")
}

func (t *RuntimeToolExecutor) completeTextModel(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, request textModelRequest) (string, map[string]any, error) {
	request.MaxOutputTokens = textModelOutputLimit(model, request.MaxOutputTokens)
	switch strings.ToUpper(strings.TrimSpace(model.Protocol)) {
	case "OPENAI_RESPONSES":
		return t.completeResponsesModel(ctx, model, provider, []contracts.ModelMessage{{Role: "system", Content: strings.TrimSpace(request.SystemPrompt)}, {Role: "user", Content: strings.TrimSpace(request.UserPrompt)}}, request.MaxOutputTokens)
	case "ANTHROPIC":
		return t.completeTextModelAnthropic(ctx, model, provider, request)
	default:
		return t.completeTextModelOpenAI(ctx, model, provider, request)
	}
}

func (t *RuntimeToolExecutor) completeTextModelOpenAI(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, request textModelRequest) (string, map[string]any, error) {
	body := map[string]any{
		"model": model.ModelID,
		"messages": []map[string]any{
			{"role": "system", "content": strings.TrimSpace(request.SystemPrompt)},
			{"role": "user", "content": strings.TrimSpace(request.UserPrompt)},
		},
		"stream": false,
	}
	modelrequest.ApplyDeterministicTemperature(body)
	if request.MaxOutputTokens > 0 {
		body["max_tokens"] = request.MaxOutputTokens
	}
	body = mergeVisionRequestCompat(body, provider, model)
	data, err := t.postModelJSON(ctx, provider, model, body, "OPENAI", "model")
	if err != nil {
		return "", nil, err
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", nil, err
	}
	if len(decoded.Choices) == 0 {
		return "", decoded.Usage, fmt.Errorf("model returned no choices")
	}
	contentText := extractVisionOpenAIContent(decoded.Choices[0].Message.Content)
	finishReason := strings.TrimSpace(decoded.Choices[0].FinishReason)
	if strings.EqualFold(finishReason, "length") {
		return contentText, decoded.Usage, textModelTruncatedError(contentText, finishReason)
	}
	if strings.TrimSpace(contentText) == "" {
		return "", decoded.Usage, textModelEmptyContentError(finishReason)
	}
	return contentText, decoded.Usage, nil
}

func (t *RuntimeToolExecutor) completeTextModelAnthropic(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, request textModelRequest) (string, map[string]any, error) {
	maxTokens := request.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1200
	}
	body := map[string]any{
		"model":      model.ModelID,
		"max_tokens": maxTokens,
		"system":     strings.TrimSpace(request.SystemPrompt),
		"messages": []map[string]any{
			{"role": "user", "content": strings.TrimSpace(request.UserPrompt)},
		},
	}
	body = mergeVisionRequestCompat(body, provider, model)
	data, err := t.postModelJSON(ctx, provider, model, body, "ANTHROPIC", "model")
	if err != nil {
		return "", nil, err
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string         `json:"stop_reason"`
		Usage      map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", nil, err
	}
	parts := make([]string, 0, len(decoded.Content))
	for _, item := range decoded.Content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "text") && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	contentText := strings.TrimSpace(strings.Join(parts, "\n"))
	stopReason := strings.TrimSpace(decoded.StopReason)
	if strings.EqualFold(stopReason, "max_tokens") {
		return contentText, decoded.Usage, textModelTruncatedError(contentText, stopReason)
	}
	if contentText == "" {
		return "", decoded.Usage, textModelEmptyContentError(stopReason)
	}
	return contentText, decoded.Usage, nil
}

func (t *RuntimeToolExecutor) postModelJSON(ctx context.Context, provider models.ProviderDefinition, model models.ModelDefinition, body map[string]any, protocol string, subject string) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := visionProviderEndpoint(provider, model, protocol)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		req.Header.Set("X-Api-Key", provider.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}
	for key, value := range visionProtocolHeaders(provider, model, protocol) {
		req.Header.Set(key, value)
	}
	client := t.httpClient
	if client == nil {
		client = httpclient.NewClient(0)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		label := strings.TrimSpace(subject)
		if label == "" {
			label = "model"
		}
		return nil, fmt.Errorf("%s request failed with status %d: %s", label, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
