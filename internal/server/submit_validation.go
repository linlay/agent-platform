package server

import (
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func (s *Server) validateSubmitRequest(req api.SubmitRequest) (contracts.AwaitingSubmitContext, error) {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.AwaitingID) == "" {
		return contracts.AwaitingSubmitContext{}, fmt.Errorf("runId and awaitingId are required")
	}
	awaiting, ok := s.deps.Runs.LookupAwaiting(req.RunID, req.AwaitingID)
	if !ok {
		return contracts.AwaitingSubmitContext{}, fmt.Errorf("unknown awaitingId")
	}
	if err := validateSubmitParams(awaiting, req.Params); err != nil {
		return contracts.AwaitingSubmitContext{}, err
	}
	return awaiting, nil
}

func validateSubmitParams(ctx contracts.AwaitingSubmitContext, params api.SubmitParams) error {
	if len(params) == 0 {
		return nil
	}
	items, err := api.DecodeSubmitParams(params)
	if err != nil {
		return err
	}
	if len(items) != len(ctx.ItemIDs) {
		return fmt.Errorf("expected %d submit items, got %d", len(ctx.ItemIDs), len(items))
	}
	seenIDs := map[string]bool{}
	for _, item := range items {
		id := strings.TrimSpace(stringValue(item["id"]))
		if id == "" {
			return fmt.Errorf("submit items.id is required")
		}
		if seenIDs[id] {
			return fmt.Errorf("duplicate submit item id: %s", id)
		}
		if _, ok := ctx.ItemIDs[id]; !ok {
			return fmt.Errorf("unknown submit item id: %s", id)
		}
		if err := validateSubmitItem(ctx.Mode, id, item); err != nil {
			return err
		}
		seenIDs[id] = true
	}
	return nil
}

func validateSubmitItem(mode string, id string, item map[string]any) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "question":
		_, hasAnswer := item["answer"]
		_, hasAnswers := item["answers"]
		if hasAnswer == hasAnswers {
			return fmt.Errorf("%s: question items require exactly one of answer or answers", id)
		}
		if _, hasDecision := item["decision"]; hasDecision {
			return fmt.Errorf("%s: question items do not allow decision", id)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: question items do not allow payload", id)
		}
	case "approval":
		decision := strings.ToLower(strings.TrimSpace(stringValue(item["decision"])))
		switch decision {
		case "approve", "reject", "approve_always":
		default:
			return fmt.Errorf("%s: approval items require decision", id)
		}
		if _, hasPayload := item["payload"]; hasPayload {
			return fmt.Errorf("%s: approval items do not allow payload", id)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: approval items do not allow answer", id)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: approval items do not allow answers", id)
		}
	case "form":
		if _, hasDecision := item["decision"]; hasDecision {
			return fmt.Errorf("%s: form items do not allow decision", id)
		}
		if _, hasAnswer := item["answer"]; hasAnswer {
			return fmt.Errorf("%s: form items do not allow answer", id)
		}
		if _, hasAnswers := item["answers"]; hasAnswers {
			return fmt.Errorf("%s: form items do not allow answers", id)
		}
		_, hasPayload := item["payload"]
		reason := strings.TrimSpace(stringValue(item["reason"]))
		if hasPayload {
			payload, ok := item["payload"].(map[string]any)
			if !ok || payload == nil {
				return fmt.Errorf("%s: form payload must be an object", id)
			}
			if reason != "" {
				return fmt.Errorf("%s: form items cannot include both payload and reason", id)
			}
		}
	default:
		return fmt.Errorf("unsupported awaiting mode: %s", mode)
	}
	return nil
}
