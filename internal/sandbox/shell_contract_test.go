package sandbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestNoStartEvidenceIsRestrictedToBeforeCommandDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(contracts.ErrSandboxCommandNotStarted.Error()))
	}))
	defer server.Close()
	service := NewContainerHubSandboxService(config.ContainerHubConfig{Enabled: true, BaseURL: server.URL, RequestTimeout: 1}, config.PathsConfig{})
	ctx := &contracts.ExecutionContext{SandboxSession: &contracts.SandboxSession{SessionID: "existing"}}
	if _, err := service.Execute(context.Background(), ctx, "echo x", "", 1, nil); !errors.Is(err, contracts.ErrSandboxCommandNotStarted) {
		t.Fatalf("local no-start evidence missing: %v", err)
	}
	if _, err := service.Execute(context.Background(), ctx, "echo x", "/workspace", 1, nil); err == nil || errors.Is(err, contracts.ErrSandboxCommandNotStarted) {
		t.Fatalf("ambiguous RPC failure claimed no start: %v", err)
	}
}
