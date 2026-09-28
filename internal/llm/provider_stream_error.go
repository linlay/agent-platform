package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/modelclient"
	"agent-platform/internal/observability"
)

// Some gateways send their ordinary error envelope inside HTTP 200 SSE. Retain
// its explicit error fields instead of mistaking it for an untyped data event.
// Diagnostics retain the observed HTTP status separately from platform status.
func providerEnvelopeError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var message string
	if json.Unmarshal(raw, &message) == nil && strings.TrimSpace(message) != "" {
		return providerReportedError("", message, "")
	}
	fields := modelclient.ParseErrorFields(raw)
	if fields.Code == "" && strings.TrimSpace(fields.Message) == "" && strings.TrimSpace(fields.Type) == "" {
		return nil
	}
	return providerReportedError(fields.Code, fields.Message, fields.Type)
}

func providerReportedError(code, message, errorType string) error {
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
	text := "provider upstream error"
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
	classified := modelclient.ClassifyError(0, code, errorType, apperrors.CodeProviderStreamFailed)
	return apperrors.New(classified, text, apperrors.WithDiagnostics(details))
}

func (s *llmRunStream) annotateProviderError(err error) error {
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) {
		return err
	}
	details := map[string]any{}
	if s.modelCall != nil {
		details["attempt"], details["maxAttempts"] = s.modelCall.attempt, s.modelCall.maxAttempts
	}
	if s.currentTurn != nil && s.currentTurn.observation.Response.StatusCode != 0 {
		details["upstreamStatus"] = s.currentTurn.observation.Response.StatusCode
	}
	upstream, _ := appErr.Payload()["diagnostics"].(map[string]any)
	for key, value := range upstream {
		details[key] = value
	}
	return apperrors.Wrap(appErr.Code(), err, apperrors.WithDiagnostics(details))
}
