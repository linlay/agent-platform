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
		_, hasPayload := item["payload"]
		reason := strings.TrimSpace(stringValue(item["reason"]))
		if hasPayload {
			payload, ok := item["payload"].(map[string]any)
			if !ok || payload == nil {
				return fmt.Errorf("%s: form payload must be an object", itemLabel)
			}
			if reason != "" {
				return fmt.Errorf("%s: form items cannot include both payload and reason", itemLabel)
			}
		}
	default:
		return fmt.Errorf("unsupported awaiting mode: %s", mode)
	}
	return nil
}
