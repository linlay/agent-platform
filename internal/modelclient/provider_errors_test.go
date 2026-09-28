package modelclient

import (
	"testing"

	"agent-platform/internal/apperrors"
)

func TestStructuredErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       apperrors.Code
	}{
		{"stream rate", `{"error":{"code":"rate_limit_exceeded","type":"too_many_requests"}}`, 200, apperrors.CodeProviderRateLimited},
		{"code wins", `{"error":{"code":"insufficient_quota","type":"rate_limit_error"}}`, 429, apperrors.CodeProviderQuotaExhausted},
		{"unknown code type fallback", `{"error":{"code":"new_code","type":"authentication_error"}}`, 200, apperrors.CodeProviderAuthFailed},
		{"message ignored", `{"error":{"message":"quota exhausted safety token limit"}}`, 429, apperrors.CodeProviderRateLimited},
		{"nested metadata ignored", `{"metadata":{"code":"insufficient_quota"}}`, 400, apperrors.CodeProviderBadRequest},
		{"exact match", `{"code":"not_rate_limit_exceeded"}`, 200, apperrors.CodeProviderRequestFailed},
		{"unknown stream", `{"error":"quota exhausted"}`, 200, apperrors.CodeProviderRequestFailed},
		{"permission", `{"type":"permission_error"}`, 200, apperrors.CodeProviderPermissionDenied},
		{"model", `{"code":"model_not_found"}`, 200, apperrors.CodeProviderModelNotFound},
		{"context", `{"code":"context_length_exceeded"}`, 200, apperrors.CodeProviderContextLengthExceeded},
		{"filter", `{"code":"content_filter"}`, 200, apperrors.CodeProviderContentFilter},
		{"overload", `{"type":"overloaded_error"}`, 200, apperrors.CodeProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := ClassifyResponseError(tc.status, tc.body)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestResponseErrorKeepsTransportStatusSeparate(t *testing.T) {
	for _, status := range []int{200, 400, 429, 500} {
		err := ResponseError(status, []byte(`{"error":{"code":"rate_limit_exceeded","type":"too_many_requests","message":"opaque message"}}`))
		payload := err.(*apperrors.Error).Payload()
		if payload["status"] != 429 || payload["retryable"] != true {
			t.Fatalf("%#v", payload)
		}
		d := payload["diagnostics"].(map[string]any)
		if d["upstreamStatus"] != status || d["upstreamType"] != "too_many_requests" || d["upstreamMessage"] != "opaque message" {
			t.Fatalf("%#v", d)
		}
	}
}
