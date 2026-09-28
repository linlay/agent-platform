package llm

import (
	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"testing"
)

func TestEOFDiagnosticsSurviveAttemptFinalization(t *testing.T) {
	for _, meaningful := range []bool{false, true} {
		_, s := responseTestStream()
		s.currentTurn.hasMeaningful = meaningful
		s.currentTurn.responseID = "resp-diagnostic"
		s.currentTurn.observation.Response.StatusCode = 200
		s.modelCall.attempt, s.modelCall.maxAttempts = 6, 6
		_, err := s.consumeCurrentTurn()
		if err == nil {
			t.Fatal("expected early EOF")
		}
		if err := s.handleModelAttemptError(err); err != nil {
			t.Fatal(err)
		}
		payload := modelErrorPayload(s.modelTerminalError)
		d, _ := payload["diagnostics"].(map[string]any)
		reason := "stream_ended_before_output"
		if meaningful {
			reason = "stream_ended_before_completion"
		}
		if d["reason"] != reason || d["attempt"] != 6 || d["maxAttempts"] != 6 || d["upstreamStatus"] != 200 || d["responseId"] != "resp-diagnostic" || d["readOutcome"] != "eof" {
			t.Fatalf("lost EOF evidence: %#v", payload)
		}
		if s.currentTurn != nil || s.modelCall != nil {
			t.Fatal("expected finalization")
		}
		found := false
		for _, delta := range s.pending {
			if discard, ok := delta.(contracts.DeltaModelTurnDiscard); ok {
				found = true
				if discard.Error["diagnostics"] == nil {
					t.Fatal("discard lost diagnostics")
				}
			}
		}
		if !found {
			t.Fatal("missing failed attempt event")
		}
	}
}

func TestAttemptAnnotationPreservesErrorContract(t *testing.T) {
	_, s := responseTestStream()
	original := apperrors.New(apperrors.CodeProviderStreamFailed, "opaque",
		apperrors.WithStatus(503), apperrors.WithRetryable(false),
		apperrors.WithDiagnostics(map[string]any{"reason": "upstream_reason", "upstreamMessage": "retained"}))
	got := modelErrorPayload(s.annotateProviderError(original))
	d := got["diagnostics"].(map[string]any)
	if got["status"] != 503 || got["retryable"] != false || d["reason"] != "upstream_reason" || d["upstreamMessage"] != "retained" {
		t.Fatalf("%#v", got)
	}
}
