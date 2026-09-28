package tools

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/models"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (t *RuntimeToolExecutor) completeResponsesModel(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, messages []contracts.ModelMessage, maxTokens int) (string, map[string]any, error) {
	input, err := modelresponses.Input(messages, model.Key)
	if err != nil {
		return "", nil, err
	}
	body := map[string]any{"model": model.ModelID, "input": input, "store": false, "stream": false}
	body = mergeVisionRequestCompat(body, provider, model)
	if maxTokens > 0 {
		body["max_output_tokens"] = maxTokens
	}
	body["input"] = input
	body["store"] = false
	body["stream"] = false
	for _, k := range []string{"previous_response_id", "conversation", "background", "messages", "max_tokens", "max_completion_tokens", "stream_options"} {
		delete(body, k)
	}
	data, err := t.postModelJSON(ctx, provider, model, body, modelresponses.Protocol, "model")
	if err != nil {
		return "", nil, err
	}
	var r modelresponses.Response
	if err = json.Unmarshal(data, &r); err != nil {
		return "", nil, err
	}
	var raw struct {
		Usage map[string]any `json:"usage"`
	}
	_ = json.Unmarshal(data, &raw)
	if r.Status != "completed" || r.Error != nil {
		return "", raw.Usage, fmt.Errorf("responses model did not complete (status=%s)", r.Status)
	}
	text := strings.TrimSpace(r.Text())
	if text == "" {
		return "", raw.Usage, fmt.Errorf("responses model returned empty content")
	}
	return text, raw.Usage, nil
}
