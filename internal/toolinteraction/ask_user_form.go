package toolinteraction

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/formhtml"
	"agent-platform/internal/hitl"
	"agent-platform/internal/stream"
	"agent-platform/internal/view"
)

type AskUserFormHandler struct{}

func NewAskUserFormHandler() *AskUserFormHandler { return &AskUserFormHandler{} }
func (h *AskUserFormHandler) ToolName() string   { return "ask_user_form" }

func (h *AskUserFormHandler) ValidateArgs(args map[string]any) error {
	for key := range args {
		if key != "title" && key != "html" && key != "values" {
			return fmt.Errorf("unsupported ask_user_form argument %q", key)
		}
	}
	title, ok := args["title"].(string)
	if !ok || strings.TrimSpace(title) == "" || len(title) > formhtml.MaxBytes {
		return fmt.Errorf("title must be a non-empty string of at most 65536 bytes")
	}
	fragment, ok := args["html"].(string)
	if !ok {
		return fmt.Errorf("html must be a string")
	}
	controls, err := formhtml.Parse(fragment)
	if err != nil {
		return err
	}
	if _, err := formhtml.NormalizeValues(args["values"], controls); err != nil {
		return err
	}

	return nil
}

func (h *AskUserFormHandler) BuildInitialAwaitAsk(toolID, runID string, tool api.ToolDetailResponse, args map[string]any, chunkIndex int, timeout int64) *stream.AwaitAsk {
	if chunkIndex != 0 || h.ValidateArgs(args) != nil {
		return nil
	}
	controls, _ := formhtml.Parse(args["html"].(string))
	values, _ := formhtml.NormalizeValues(args["values"], controls)
	return &stream.AwaitAsk{
		AwaitingID: toolID,
		RunID:      runID,
		Mode:       "form",
		View:       view.Builtin("ask_user_form"),
		Timeout:    timeout,
		Form: map[string]any{
			"title": args["title"],
			"data": map[string]any{
				"html":   args["html"],
				"values": values,
			},
		},
	}
}

func (h *AskUserFormHandler) NormalizeSubmit(args map[string]any, param any) (map[string]any, error) {
	if err := h.ValidateArgs(args); err != nil {
		return nil, err
	}
	normalized, err := hitl.NormalizeForm(nil, param)
	if err != nil {
		return nil, err
	}
	entry, ok := normalized["form"].(map[string]any)
	if !ok {
		return normalized, nil
	}
	entry["title"] = args["title"]
	if reason, _ := entry["reason"].(string); len(reason) > formhtml.MaxBytes {
		return nil, fmt.Errorf("reason exceeds 65536 bytes")
	}
	if data, ok := entry["data"].(map[string]any); ok {
		controls, err := formhtml.Parse(args["html"].(string))
		if err != nil {
			return nil, err
		}
		entry["data"], err = formhtml.ValidateData(data, controls, true)
		if err != nil {
			return nil, err
		}
	}
	return normalized, nil
}

func (h *AskUserFormHandler) FormatModelOutput(result contracts.ToolExecutionResult) string {
	structured := contracts.AnyMapNode(result.Structured)
	form := contracts.AnyMapNode(structured["form"])
	if len(form) == 0 {
		return result.Output
	}
	text := "表单《" + contracts.AnyStringNode(form["title"]) + "》已提交"
	if form["decision"] == "reject" {
		text = "用户拒绝填写表单《" + contracts.AnyStringNode(form["title"]) + "》"
	}
	if reason := contracts.AnyStringNode(form["reason"]); reason != "" {
		text += "：" + reason
	}
	if data, ok := form["data"]; ok {
		raw, _ := json.Marshal(data)
		text += "\n" + string(raw)
	}
	return text
}
