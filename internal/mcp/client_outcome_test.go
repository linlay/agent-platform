package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/contracts"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise actual HTTP failures after the server receives tools/call, rather
// than injecting an error into the gateway's McpClient interface.
func TestClientCallOutcomeAfterTransportFailure(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect", "rpc_error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := sdk.NewServer(&sdk.Implementation{Name: "outcome", Version: "1"}, nil)
			handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return upstream }, &sdk.StreamableHTTPOptions{JSONResponse: true})
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if err := json.Unmarshal(data, &request); err != nil {
						t.Error(err)
						return
					}
					if request.Method == "tools/call" {
						calls.Add(1)
						switch mode {
						case "timeout":
							<-r.Context().Done()
						case "disconnect":
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
						case "rpc_error":
							w.Header().Set("Content-Type", "application/json")
							_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "arguments rejected"}})
						}
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(data))
				}
				handler.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			root := t.TempDir()
			if err := writeConnectorFixture(filepath.Join(root, "server.yml"), []byte("key: demo\nbaseUrl: "+endpoint.URL+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			registry, err := NewRegistry(root)
			if err != nil {
				t.Fatal(err)
			}
			client := NewClientWithGate(registry, endpoint.Client(), nil)
			defer client.Close()
			if err := client.Initialize(t.Context(), "demo"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			_, err = client.CallTool(ctx, "demo", "read", nil, nil)
			if err == nil || !errors.Is(err, contracts.ErrMCPCallFailed) {
				t.Fatalf("expected call failure, got %v", err)
			}
			if unknown := errors.Is(err, contracts.ErrMCPCallOutcomeUnknown); unknown != (mode != "rpc_error") {
				t.Fatalf("unknown=%v for %s: %v", unknown, mode, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("tools/call was retried: %d requests", calls.Load())
			}
			if _, err := client.CallTool(t.Context(), "not-mounted", "read", nil, nil); errors.Is(err, contracts.ErrMCPCallOutcomeUnknown) {
				t.Fatalf("pre-dispatch failure marked unknown: %v", err)
			}
		})
	}
}
