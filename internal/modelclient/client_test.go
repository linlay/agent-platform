package modelclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
