package llm

import (
	"encoding/json"
	"errors"
	"maps"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/modelresponses"
)

type responsesStreamEvent struct {
	Type         string                  `json:"type"`
	OutputIndex  int                     `json:"output_index"`
	SummaryIndex int                     `json:"summary_index"`
	Delta        string                  `json:"delta"`
	Item         modelresponses.Item     `json:"item"`
	Response     modelresponses.Response `json:"response"`
	Code         string                  `json:"code"`
	Message      string                  `json:"message"`
	Error        json.RawMessage         `json:"error"`
}

func responsesDecodeError(err error) error {
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	details := map[string]any{}
	switch {
	case errors.As(err, &syntax):
		details["jsonErrorOffset"] = syntax.Offset
	case errors.As(err, &mismatch):
		details["jsonErrorOffset"] = mismatch.Offset
	}
	return responsesInvalid("decode responses event", apperrors.WithDiagnostics(details))
}

func annotateResponsesError(err error, call *pendingModelCall, turn *providerTurnStream, event responsesStreamEvent, eventName, raw string) error {
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) {
		return err
	}
	// This context is safe for always-on logs: structure/counts only, never raw
	// frames, model text, arguments, encrypted reasoning or upstream messages.
	context := map[string]any{
		"protocol": modelresponses.Protocol, "eventType": diagnosticLabel(event.Type),
		"sseEvent": diagnosticLabel(eventName), "frameBytes": len(raw),
	}
	if appErr.Code() == apperrors.CodeProviderStreamInvalid {
		context["validationError"] = diagnosticLabel(appErr.Error())
	}
	if call != nil {
		context["attempt"], context["maxAttempts"] = call.attempt, call.maxAttempts
	}
	if turn != nil {
		context["frameNumber"] = turn.observation.Frames
		if metadata := turn.observation.Response; metadata.StatusCode != 0 {
			context["upstreamStatus"] = metadata.StatusCode
			if metadata.RequestID != "" {
				context["upstreamRequestId"] = metadata.RequestID
			}
		}
		responseID := event.Response.ID
		if responseID == "" {
			responseID = turn.responseID
		}
		if responseID != "" {
			context["responseId"] = diagnosticLabel(responseID)
		}
		if state := turn.responses; state != nil {
			context["observedItemCount"] = len(state.items)
			context["observedDoneItemCount"] = len(state.done)
		}
	}
	switch event.Type {
	case "response.completed", "response.incomplete", "response.failed":
		context["responseStatus"] = diagnosticLabel(event.Response.Status)
		context["terminalOutputCount"] = len(event.Response.Output)
		if reason := event.Response.IncompleteDetails.Reason; reason != "" {
			context["incompleteReason"] = diagnosticLabel(reason)
		}
	}
	// Only report known field names. Arbitrary JSON keys can themselves contain
	// prompt text or credentials; count unknown fields without copying them.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) == nil && fields != nil {
		known := []string{}
		for _, key := range []string{"type", "error", "response", "choices", "usage", "object", "id", "message", "code", "delta", "item", "output_index", "sequence_number", "status"} {
			if _, ok := fields[key]; ok {
				known = append(known, key)
			}
		}
		context["frameFields"] = known
		context["otherFieldCount"] = len(fields) - len(known)
	}
	details, _ := appErr.Payload()["diagnostics"].(map[string]any)
	for _, key := range []string{"outputIndex", "observedItemType", "observedItemDone", "finalItemType", "itemIDChanged", "jsonErrorOffset"} {
		if value, ok := details[key]; ok {
			context[key] = value
		}
	}
	if turn != nil {
		turn.observation.ResponsesFailure = context
	}
	publicDetails := maps.Clone(context)
	maps.Copy(publicDetails, details)
	return apperrors.Wrap(appErr.Code(), err, apperrors.WithDiagnostics(publicDetails))
}
