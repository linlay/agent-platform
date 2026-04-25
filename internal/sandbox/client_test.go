package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
)

func TestCreateSessionIncludesContainerHubErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/create" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"validation failed: mount source does not exist: /missing-pan"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	_, err := client.CreateSession(context.Background(), map[string]any{"session_id": "run-test"})
	if err == nil {
		t.Fatal("CreateSession() expected error")
	}
	if !strings.Contains(err.Error(), "/api/sessions/create returned status 400: validation failed: mount source does not exist: /missing-pan") {
		t.Fatalf("CreateSession() error = %q", err.Error())
	}
}

func TestExecuteSessionIncludesContainerHubErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"session is stopped; recreate it before executing commands"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	_, _, err := client.ExecuteSessionRaw(context.Background(), "run-test", map[string]any{"command": "/bin/sh"})
	if err == nil {
		t.Fatal("ExecuteSessionRaw() expected error")
	}
	if !strings.Contains(err.Error(), "/api/sessions/execute returned status 409: session is stopped; recreate it before executing commands") {
		t.Fatalf("ExecuteSessionRaw() error = %q", err.Error())
	}
}
