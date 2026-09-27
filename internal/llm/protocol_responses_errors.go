package llm

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/modelresponses"
	"agent-platform/internal/observability"
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

// Some gateways send their ordinary error envelope inside HTTP 200 SSE. Retain
// its explicit error fields instead of mistaking it for an untyped data event.
// The HTTP status remains the observed transport status; do not invent a 503.
func responsesEnvelopeError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var message string
	if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message) != "" {
		return responsesReportedError("", message, "")
	}
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Type    string          `json:"type"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	var code string
	if json.Unmarshal(envelope.Code, &code) != nil {
		var number json.Number
		if json.Unmarshal(envelope.Code, &number) == nil {
			code = number.String()
		}
	}
	if code == "" && strings.TrimSpace(envelope.Message) == "" && strings.TrimSpace(envelope.Type) == "" {
		return nil
	}
	return responsesReportedError(code, envelope.Message, envelope.Type)
}

func responsesReportedError(code, message, errorType string) error {
	code, errorType = diagnosticLabel(code), diagnosticLabel(errorType)
	message = observability.SanitizeLog(strings.TrimSpace(message))
	if len(message) > 1024 {
		message = message[:1024]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += "…"
	}
	details := map[string]any{}
	text := "responses upstream error"
	if code != "" {
		text += " " + code
		details["upstreamCode"] = code
	}
	if errorType != "" {
		details["upstreamType"] = errorType
	}
	if message != "" {
		text += ": " + message
		details["upstreamMessage"] = message
	}
	return apperrors.New(apperrors.CodeProviderStreamFailed, text, apperrors.WithDiagnostics(details))
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
	if !errors.As(err, &appErr) || (appErr.Code() != apperrors.CodeProviderStreamInvalid && appErr.Code() != apperrors.CodeProviderStreamFailed) {
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
