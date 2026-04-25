package server

import (
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func validateSubmitIdentity(req api.SubmitRequest) error {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return fmt.Errorf("runId and awaitingId are required")
	}
	return nil
}

func (s *Server) lookupActiveAwaiting(req api.SubmitRequest) (contracts.AwaitingSubmitContext, bool) {
	if s == nil || s.deps.Runs == nil {
		return contracts.AwaitingSubmitContext{}, false
	}
	return s.deps.Runs.LookupAwaiting(req.RunID, req.AwaitingID)
}

func validateSubmitParams(ctx contracts.AwaitingSubmitContext, params api.SubmitParams) error {
	if len(params) == 0 {
		return nil
	}
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	if len(items) != ctx.ItemCount {
		return fmt.Errorf("expected %d submit items, got %d", ctx.ItemCount, len(items))
	}
	for index, item := range items {
		if err := validateSubmitItem(ctx.Mode, index, item); err != nil {
			return err
		}
	}
	return nil
}

func validateDeferredSubmitParams(mode string, params api.SubmitParams) error {
	if len(params) == 0 {
		return nil
	}
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	for index, item := range items {
		if err := validateSubmitItem(mode, index, item); err != nil {
			return err
		}
	}
	return nil
}

func validateSubmitItem(mode string, index int, item map[string]any) error {
	itemLabel := fmt.Sprintf("items[%d]", index)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		_, hasAnswer := item["answer"]
		_, hasAnswers := item["answers"]
		if hasAnswer == hasAnswers {
			return fmt.Errorf("%s: question items require exactly one of answer or answers", itemLabel)
		}
		if _, hasDecision := item["decision"]; hasDecision {
			return fmt.Errorf("%s: question items do not allow decision", itemLabel)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: question items do not allow payload", itemLabel)
		}
	case "approval":
		decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
		if decision == "" {
			return fmt.Errorf("%s: approval items require decision", itemLabel)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: approval items do not allow payload", itemLabel)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: approval items do not allow answer", itemLabel)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: approval items do not allow answers", itemLabel)
		}
	case "form":
		if _, hasDecision := item["decision"]; hasDecision {
			return fmt.Errorf("%s: form items do not allow decision", itemLabel)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: form items do not allow answer", itemLabel)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: form items do not allow answers", itemLabel)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: form items do not allow payload", itemLabel)
		}
		if _, hasReason := item["reason"]; hasReason {
			return fmt.Errorf("%s: form items do not allow reason", itemLabel)
		}
		action := strings.ToLower(strings.TrimSpace(stringValue(item["action"])))
		if action == "" {
			return fmt.Errorf("%s: form items require action", itemLabel)
		}
		if rawForm, hasForm := item["form"]; hasForm {
			form, ok := rawForm.(map[string]any)
			if !ok || form == nil {
				return fmt.Errorf("%s: form field must be an object", itemLabel)
			}
		}
		switch action {
		case "submit":
			if _, hasForm := item["form"]; !hasForm {
				return fmt.Errorf("%s: submit action requires form", itemLabel)
			}
		case "reject", "cancel":
		default:
			return fmt.Errorf("%s: unsupported form action %q", itemLabel, action)
		}
	default:
		return fmt.Errorf("unsupported awaiting mode: %s", mode)
	}
	return nil
}
