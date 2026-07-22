package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/config"
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

func TestProviderAuthFailureIsNotExposedAsEndUserUnauthorized(t *testing.T) {
	err := providerResponseError(http.StatusUnauthorized, []byte(`{"error":{"message":"invalid api key"}}`))
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %T", err)
	}
	payload := appErr.Payload()
	if payload["code"] != string(apperrors.CodeProviderAuthFailed) || payload["status"] != http.StatusBadGateway {
		t.Fatalf("unexpected provider auth payload %#v", payload)
	}
}

func TestExecuteProviderRequestUsesFirstResponseTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	}))
	defer server.Close()

	engine := NewLLMAgentEngineWithHTTPClient(config.Config{}, nil, nil, nil, nil, server.Client())
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	start := time.Now()
	_, err = engine.executeProviderRequest(req, time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected first response timeout")
	}
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) || appErr.Code() != apperrors.CodeProviderTimeout {
		t.Fatalf("expected provider timeout app error, got %T %v", err, err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("expected first response timeout near 1s, took %s", elapsed)
	}
}
