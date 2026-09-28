package modelclient

import (
	"encoding/json"
	"net/http"
	"strings"

	"agent-platform/internal/apperrors"
)

// ErrorFields contains provider diagnostics, not localized user messages.
type ErrorFields struct {
	Code    string
	Type    string
	Message string
}

// ParseErrorFields reads only defined error locations. Message text and arbitrary
// nested objects must never become classification signals.
func ParseErrorFields(body []byte) ErrorFields {
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ErrorFields{}
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var message string
		if json.Unmarshal(envelope.Error, &message) == nil {
			return ErrorFields{Message: message}
		}
		return ParseErrorFields(envelope.Error)
	}
	var code string
	if json.Unmarshal(envelope.Code, &code) != nil {
		var number json.Number
		if json.Unmarshal(envelope.Code, &number) == nil {
			code = number.String()
		}
	}
	return ErrorFields{Code: code, Type: envelope.Type, Message: envelope.Message}
}

// ClassifyError uses exact structured codes before types, then HTTP status.
// HTTP 200 only confirms stream establishment; fallback describes unknown errors.
func ClassifyError(status int, code, errorType string, fallback apperrors.Code) apperrors.Code {
	for _, signal := range []string{code, errorType} {
		switch strings.TrimSpace(signal) {
		case "rate_limit_exceeded", "rate_limit_error", "too_many_requests":
			return apperrors.CodeProviderRateLimited
		case "insufficient_quota":
			return apperrors.CodeProviderQuotaExhausted
		case "invalid_api_key", "authentication_error":
			return apperrors.CodeProviderAuthFailed
		case "permission_denied", "permission_error":
			return apperrors.CodeProviderPermissionDenied
		case "model_not_found":
			return apperrors.CodeProviderModelNotFound
		case "context_length_exceeded":
			return apperrors.CodeProviderContextLengthExceeded
		case "content_filter":
			return apperrors.CodeProviderContentFilter
		case "overloaded_error", "server_error":
			return apperrors.CodeProviderUnavailable
		}
	}
	switch {
	case status == http.StatusUnauthorized:
		return apperrors.CodeProviderAuthFailed
	case status == http.StatusForbidden:
		return apperrors.CodeProviderPermissionDenied
	case status == http.StatusNotFound:
		return apperrors.CodeProviderModelNotFound
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return apperrors.CodeProviderTimeout
	case status == http.StatusTooManyRequests:
		return apperrors.CodeProviderRateLimited
	case status == http.StatusRequestEntityTooLarge:
		return apperrors.CodeProviderContextLengthExceeded
	case status >= 500:
		return apperrors.CodeProviderUnavailable
	case status >= 400:
		return apperrors.CodeProviderBadRequest
	default:
		return fallback
	}
}
