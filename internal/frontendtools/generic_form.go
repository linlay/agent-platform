package frontendtools

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type GenericFormHandler struct{}

func NewGenericFormHandler() *GenericFormHandler {
	return &GenericFormHandler{}
}

func (h *GenericFormHandler) ToolName() string {
	return "*"
}

func (h *GenericFormHandler) ValidateArgs(_ map[string]any) error {
	return nil
}

func (h *GenericFormHandler) BuildInitialAwaitAsk(toolID string, runID string, tool api.ToolDetailResponse, args map[string]any, chunkIndex int, timeout int64) *stream.AwaitAsk {
	if chunkIndex != 0 {
		return nil
	}
	viewportType := strings.TrimSpace(contracts.AnyStringNode(tool.Meta["viewportType"]))
	if viewportType == "" {
		viewportType = "html"
	}
	viewportKey := strings.TrimSpace(contracts.AnyStringNode(tool.Meta["viewportKey"]))
	if viewportKey == "" {
		viewportKey = strings.TrimSpace(tool.Name)
	}
	form := map[string]any{
		"id":       "form-1",
		"toolName": strings.TrimSpace(tool.Name),
		"form":     contracts.CloneMap(args),
	}
	if label := strings.TrimSpace(tool.Label); label != "" {
		form["title"] = label
	}
	return &stream.AwaitAsk{
		AwaitingID:   toolID,
		ViewportType: viewportType,
		ViewportKey:  viewportKey,
		Mode:         "form",
		Timeout:      timeout,
		RunID:        runID,
		Forms:        []any{form},
	}
}

func (h *GenericFormHandler) NormalizeSubmit(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeSubmitItems(params)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return contracts.AwaitingErrorAnswer("form", "user_dismissed", "用户关闭等待项"), nil
	}
	forms := make([]map[string]any, 0, len(items))
	for index, item := range items {
		id := strings.TrimSpace(contracts.AnyStringNode(item["id"]))
		if id == "" {
			id = "form-1"
		}
		decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
		if decision == "" {
			decision = "submit"
		}
		entry := map[string]any{
			"id":       id,
			"decision": decision,
		}
		if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
			entry["reason"] = reason
		}
		if form := genericSubmittedForm(item); len(form) > 0 {
			entry["form"] = form
		} else if len(item) > 0 {
			entry["form"] = contracts.CloneMap(item)
		} else {
			entry["form"] = contracts.CloneMap(args)
		}
		if index == 0 {
			entry["toolArgs"] = contracts.CloneMap(args)
		}
		forms = append(forms, entry)
	}
	return map[string]any{
		"mode":   "form",
		"status": "answered",
		"forms":  forms,
	}, nil
}

func (h *GenericFormHandler) FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "json", "json-compact":
		if len(result.Structured) == 0 {
			return result.Output, true
		}
		data, err := json.Marshal(result.Structured)
		if err != nil {
			return result.Output, true
		}
		return string(data), true
	default:
		return "", false
	}
}

func genericSubmittedForm(item map[string]any) map[string]any {
	for _, key := range []string{"form", "payload", "value", "answer"} {
		if form := contracts.AnyMapNode(item[key]); len(form) > 0 {
			return form
		}
	}
	return nil
}
