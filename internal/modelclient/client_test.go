package modelclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/apperrors"
)

func TestOpenStreamReturnsSuccessfulBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer upstream.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := New(upstream.Client()).OpenStream(request, time.Second)
	if err != nil || stream == nil || stream.Body == nil {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
	defer stream.Body.Close()
	if stream.Cancel != nil {
		defer stream.Cancel()
	}
}

func TestResponseErrorClassifiesProviderQuota(t *testing.T) {
	err := ResponseError(http.StatusTooManyRequests, []byte(`{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`))
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) || appErr.Code() != apperrors.CodeProviderQuotaExhausted {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestOpenStreamRetainsOnlyBoundedResponseMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", strings.Repeat("a", 300))
		w.Header().Set("Set-Cookie", "private-cookie")
		w.Header().Set("Authorization", "Bearer private-token")
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer upstream.Close()
	for _, timeout := range []time.Duration{0, time.Second} {
		request, _ := http.NewRequest(http.MethodPost, upstream.URL, nil)
		stream, err := New(upstream.Client()).OpenStream(request, timeout)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Body.Close()
		if stream.Cancel != nil {
			stream.Cancel()
		}
		metadata := stream.Response
		if metadata.StatusCode != 200 || metadata.ContentType != "text/event-stream" || len(metadata.RequestID) != 256 || metadata.RequestIDHeader != "X-Request-Id" {
			t.Fatalf("unexpected metadata: %#v", metadata)
		}
		data, _ := json.Marshal(metadata)
		if strings.Contains(string(data), "private-") {
			t.Fatalf("unexpected headers retained: %s", data)
		}
	}
}
