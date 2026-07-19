package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientStrictlyRejectsNegotiatedProtocolVersions(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{name: "legacy", version: "2025-06-18"},
		{name: "missing", version: ""},
		{name: "invalid", version: "not-a-version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &versionMCPServer{version: test.version}
			httpServer := httptest.NewServer(fake)
			root := t.TempDir()
			writeMCPRegistryFile(t, filepath.Join(root, "server.yml"), "serverKey: strict\nbaseUrl: "+httpServer.URL+"\nretry: 0\n")
			registry, err := NewRegistry(root)
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			gate := NewAvailabilityGate()
			client := NewClientWithGate(registry, httpServer.Client(), gate)
			err = client.Initialize(t.Context(), "strict")
			_ = client.Close()
			httpServer.Close()
			if err == nil || !strings.Contains(err.Error(), ProtocolVersion) {
				t.Fatalf("Initialize error = %v, want required version %s", err, ProtocolVersion)
			}
			if !gate.IsUnavailable("strict") {
				t.Fatal("version-incompatible server was not placed in the availability gate")
			}
			deadline := time.Now().Add(time.Second)
			for fake.closed.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if fake.closed.Load() == 0 {
				t.Fatal("incompatible MCP session was not closed")
			}
		})
	}
}

type versionMCPServer struct {
	version string
	closed  atomic.Int32
}

func (s *versionMCPServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodDelete:
		s.closed.Add(1)
		writer.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
		return
	case http.MethodPost:
		body, _ := io.ReadAll(request.Body)
		var message struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &message) != nil {
			http.Error(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		if message.Method == "notifications/initialized" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		if message.Method != "initialize" {
			http.Error(writer, "unexpected method", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Mcp-Session-Id", "strict-session")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      message.ID,
			"result": map[string]any{
				"protocolVersion": s.version,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "version-test", "version": "1.0.0"},
			},
		})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}
