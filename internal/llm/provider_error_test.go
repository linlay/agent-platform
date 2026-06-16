package llm

import (
	"errors"
	"testing"

	"agent-platform/internal/apperrors"
)

func TestClassifyProviderResponseError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   apperrors.Code
	}{
		{
			name:   "quota exhausted",
			status: 429,
			body:   `{"error":"api key quota exhausted"}`,
			want:   apperrors.CodeProviderQuotaExhausted,
		},
		{
			name:   "rate limited",
			status: 429,
			body:   `{"error":{"code":"rate_limit_exceeded","message":"too many requests"}}`,
			want:   apperrors.CodeProviderRateLimited,
		},
		{
			name:   "auth failed",
			status: 401,
			body:   `{"error":{"message":"invalid api key"}}`,
			want:   apperrors.CodeProviderAuthFailed,
		},
		{
			name:   "context length",
			status: 400,
			body:   `{"error":{"code":"context_length_exceeded","message":"too many tokens"}}`,
			want:   apperrors.CodeProviderContextLengthExceeded,
		},
		{
			name:   "content filter",
			status: 400,
			body:   `{"error":{"code":"content_filter","message":"blocked by safety policy"}}`,
			want:   apperrors.CodeProviderContentFilter,
		},
		{
			name:   "server unavailable",
			status: 502,
			body:   `bad gateway`,
			want:   apperrors.CodeProviderUnavailable,
		},
		{
			name:   "bad json request",
			status: 400,
			body:   `{`,
			want:   apperrors.CodeProviderBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classifyProviderResponseError(tt.status, tt.body)
			if got != tt.want {
				t.Fatalf("classifyProviderResponseError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderResponseErrorCarriesStructuredPayload(t *testing.T) {
	err := providerResponseError(429, []byte(`{"error":"api key quota exhausted"}`))
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %T", err)
	}
	payload := appErr.Payload()
	if payload["code"] != string(apperrors.CodeProviderQuotaExhausted) || payload["category"] != string(apperrors.CategoryModel) {
		t.Fatalf("unexpected provider payload %#v", payload)
	}
	if payload["status"] != 429 || payload["retryable"] != false {
		t.Fatalf("unexpected provider metadata %#v", payload)
	}
	diagnostics, _ := payload["diagnostics"].(map[string]any)
	if diagnostics["upstreamStatus"] != 429 {
		t.Fatalf("expected upstream status diagnostics, got %#v", diagnostics)
	}
}
