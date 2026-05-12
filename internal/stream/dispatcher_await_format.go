package stream

import (
	"log"
	"strings"
)

func newAwaitingAnswerEvent(input AwaitingAnswer) StreamEvent {
	answer := clonePayload(input.Answer)
	if len(answer) == 0 {
		return StreamEvent{}
	}
	mode := strings.ToLower(strings.TrimSpace(anyString(answer["mode"])))
	if mode == "" {
		return StreamEvent{}
	}
	payload := map[string]any{
		"awaitingId": input.AwaitingID,
		"mode":       mode,
	}
	status := strings.ToLower(strings.TrimSpace(anyString(answer["status"])))
	if status == "" {
		return StreamEvent{}
	}
	payload["status"] = status
	if status == "error" {
		if errPayload := anyMap(answer["error"]); len(errPayload) > 0 {
			entry := map[string]any{}
			if code := strings.TrimSpace(anyString(errPayload["code"])); code != "" {
				entry["code"] = code
			}
			if message := strings.TrimSpace(anyString(errPayload["message"])); message != "" {
				entry["message"] = message
			}
			if len(entry) > 0 {
				payload["error"] = entry
			}
		}
		return NewEvent("awaiting.answer", payload)
	}
	if status != "answered" {
		return StreamEvent{}
	}
	switch mode {
	case "question":
		formatted := formatAwaitingAnswers(answer["answers"])
		if len(formatted) > 0 {
			payload["answers"] = formatted
		}
	case "approval":
		formatted := formatAwaitingApprovals(answer["approvals"])
		if len(formatted) > 0 {
			payload["approvals"] = formatted
		}
	case "form":
		formatted := formatAwaitingForms(answer["forms"])
		if len(formatted) > 0 {
			payload["forms"] = formatted
		}
	default:
		return StreamEvent{}
	}
	return NewEvent("awaiting.answer", payload)
}

func formatAwaitingAnswers(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingAnswers(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			answer, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(anyString(answer["id"]))
			if id == "" {
				continue
			}
			entry := map[string]any{
				"id": id,
			}
			if question := strings.TrimSpace(anyString(answer["question"])); question != "" {
				entry["question"] = question
			}
			if header := strings.TrimSpace(anyString(answer["header"])); header != "" {
				entry["header"] = header
			}
			appendAwaitingQuestionValue(entry, answer["answer"])
			if value := strings.TrimSpace(anyString(answer["value"])); value != "" {
				entry["value"] = value
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func formatAwaitingApprovals(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingApprovals(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			approval := anyMap(item)
			id := strings.TrimSpace(anyString(approval["id"]))
			decision := strings.ToLower(strings.TrimSpace(anyString(approval["decision"])))
			if id == "" || decision == "" {
				continue
			}
			entry := map[string]any{
				"id":       id,
				"decision": decision,
			}
			if command := strings.TrimSpace(anyString(approval["command"])); command != "" {
				entry["command"] = command
			}
			if reason := strings.TrimSpace(anyString(approval["reason"])); reason != "" {
				entry["reason"] = reason
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func formatAwaitingForms(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return formatAwaitingForms(items)
	case []any:
		formatted := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			form := anyMap(item)
			id := strings.TrimSpace(anyString(form["id"]))
			decision := strings.ToLower(strings.TrimSpace(anyString(form["decision"])))
			if id == "" || decision == "" {
				continue
			}
			entry := map[string]any{
				"id":       id,
				"decision": decision,
			}
			if submittedForm, ok := form["form"].(map[string]any); ok && len(submittedForm) > 0 {
				entry["form"] = clonePayload(submittedForm)
			}
			if command := strings.TrimSpace(anyString(form["command"])); command != "" {
				entry["command"] = command
			}
			if reason := strings.TrimSpace(anyString(form["reason"])); reason != "" {
				entry["reason"] = reason
			}
			formatted = append(formatted, entry)
		}
		return formatted
	default:
		return nil
	}
}

func appendAwaitingQuestionValue(entry map[string]any, value any) {
	switch typed := value.(type) {
	case []string:
		entry["answers"] = append([]string(nil), typed...)
	case []any:
		entry["answers"] = append([]any(nil), typed...)
	default:
		entry["answer"] = value
	}
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func anyMap(value any) map[string]any {
	item, _ := value.(map[string]any)
	return item
}

func (d *StreamEventDispatcher) newAwaitAskEvent(input AwaitAsk) StreamEvent {
	awaitingID := strings.TrimSpace(input.AwaitingID)
	if awaitingID == "" {
		return StreamEvent{}
	}
	if d.state.emittedAwaitings[awaitingID] {
		log.Printf("[stream][run:%s][warn] duplicate awaiting.ask ignored awaitingId=%s", d.request.RunID, awaitingID)
		return StreamEvent{}
	}
	d.state.emittedAwaitings[awaitingID] = true
	payload := map[string]any{
		"awaitingId": awaitingID,
		"mode":       input.Mode,
		"timeout":    input.Timeout,
		"runId":      input.RunID,
	}
	if strings.EqualFold(strings.TrimSpace(input.Mode), "form") {
		if strings.TrimSpace(input.ViewportType) != "" {
			payload["viewportType"] = input.ViewportType
		}
		if strings.TrimSpace(input.ViewportKey) != "" {
			payload["viewportKey"] = input.ViewportKey
		}
	}
	if len(input.Questions) > 0 {
		payload["questions"] = input.Questions
	}
	if len(input.Approvals) > 0 {
		payload["approvals"] = input.Approvals
	}
	if len(input.Forms) > 0 {
		payload["forms"] = input.Forms
	}
	return NewEvent("awaiting.ask", payload)
}
