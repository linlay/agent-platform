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

func TestClientNegotiatesSupportedProtocolVersions(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
		t.Run(version, func(t *testing.T) {
			fake := &versionMCPServer{t: t, version: version, checkHeaders: true}
			httpServer := httptest.NewServer(fake)
			defer httpServer.Close()
			root := t.TempDir()
			writeMCPRegistryFile(t, filepath.Join(root, "server.yml"), "serverKey: compatible\nbaseUrl: "+httpServer.URL+"\nretry: 0\n")
			registry, err := NewRegistry(root)
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			gate := NewAvailabilityGate()
			client := NewClientWithGate(registry, httpServer.Client(), gate)
			defer client.Close()
			syncer := NewToolSync(registry, client)
			tools, err := syncer.Load(t.Context())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			status, ok := syncer.ServerStatus("compatible")
			if !ok || status.Status != ToolSyncStatusReady {
				t.Fatalf("server status = %#v, want ready", status)
			}
			if len(tools) != 1 || tools[0].Name != "read_document" {
				t.Fatalf("synchronized tools = %#v", tools)
			}
			result, err := client.CallTool(t.Context(), "compatible", "read_document", map[string]any{}, nil)
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			data, _ := json.Marshal(result)
			if !strings.Contains(string(data), "document contents") {
				t.Fatalf("CallTool result = %s", data)
			}
			if gate.IsBlocked("compatible") {
				t.Fatal("compatible server was placed in the availability gate")
			}
			if fake.initializations.Load() != 1 || fake.initialized.Load() != 1 {
				t.Fatalf("session was not reused: initialize=%d initialized=%d", fake.initializations.Load(), fake.initialized.Load())
			}
		})
	}
}

func TestClientRejectsUnsupportedNegotiatedProtocolVersions(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{name: "unknown", version: "2099-01-01"},
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
			if err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
				t.Fatalf("Initialize error = %v, want unsupported protocol version", err)
			}
			if !gate.IsBlocked("strict") {
				t.Fatal("version-incompatible server was not placed in the availability gate")
			}
			deadline := time.Now().Add(time.Second)
			for fake.closed.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if fake.closed.Load() == 0 {
				t.Fatal("incompatible MCP session was not closed")
			}
			if fake.initialized.Load() != 0 {
				t.Fatal("initialized notification was sent for an unsupported version")
			}
		})
	}
}

func TestClientPreservesInitializationRateLimitError(t *testing.T) {
	fake := &versionMCPServer{t: t, version: "2025-03-26", checkHeaders: true, initializedStatus: http.StatusTooManyRequests}
	httpServer := httptest.NewServer(fake)
	defer httpServer.Close()
	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "server.yml"), "serverKey: limited\nbaseUrl: "+httpServer.URL+"\nretry: 0\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, httpServer.Client(), gate)
	defer client.Close()
	syncer := NewToolSync(registry, client)
	tools, err := syncer.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	status, ok := syncer.ServerStatus("limited")
	if !ok || status.Status != ToolSyncStatusUnavailable || status.Diagnostic == nil ||
		!strings.Contains(status.Diagnostic.Message, "notifications/initialized") ||
		!strings.Contains(status.Diagnostic.Message, "Too Many Requests") {
		t.Fatalf("server status = %#v, want initialization rate-limit diagnostic", status)
	}
	if len(tools) != 0 || !gate.IsBlocked("limited") || fake.closed.Load() == 0 {
		t.Fatalf("failed session was not isolated: tools=%d blocked=%v closed=%d", len(tools), gate.IsBlocked("limited"), fake.closed.Load())
	}
}

type versionMCPServer struct {
	t                 *testing.T
	version           string
	checkHeaders      bool
	initializedStatus int
	closed            atomic.Int32
	initializations   atomic.Int32
	initialized       atomic.Int32
}

func (s *versionMCPServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	checkHeaders := func() bool {
		if s.checkHeaders {
			if got := request.Header.Get("Mcp-Protocol-Version"); got != s.version {
				s.t.Errorf("%s protocol header = %q, want negotiated version %q", request.Method, got, s.version)
				http.Error(writer, "incorrect protocol version header", http.StatusBadRequest)
				return false
			}
			if got := request.Header.Get("Mcp-Session-Id"); got != "version-session" {
				s.t.Errorf("%s session header = %q", request.Method, got)
				http.Error(writer, "incorrect session header", http.StatusBadRequest)
				return false
			}
		}
		return true
	}
	switch request.Method {
	case http.MethodDelete:
		if !checkHeaders() {
			return
		}
		s.closed.Add(1)
		writer.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		if !checkHeaders() {
			return
		}
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
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &message) != nil {
			http.Error(writer, "invalid JSON", http.StatusBadRequest)
			return
		}
		if message.Method != "initialize" && !checkHeaders() {
			return
		}
		var result any
		switch message.Method {
		case "notifications/initialized":
			s.initialized.Add(1)
			if s.initializedStatus != 0 {
				http.Error(writer, http.StatusText(s.initializedStatus), s.initializedStatus)
				return
			}
			writer.WriteHeader(http.StatusAccepted)
			return
		case "initialize":
			s.initializations.Add(1)
			if s.checkHeaders {
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if err := json.Unmarshal(message.Params, &params); err != nil || params.ProtocolVersion != ProtocolVersion {
					s.t.Errorf("initialize params = %s, want preferred version %s", message.Params, ProtocolVersion)
				}
			}
			writer.Header().Set("Mcp-Session-Id", "version-session")
			result = map[string]any{
				"protocolVersion": s.version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "version-test", "version": "1.0.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "read_document", "inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "document contents"}}}
		default:
			http.Error(writer, "unexpected method", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      message.ID,
			"result":  result,
		})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}
