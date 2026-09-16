package mcp

import (
	"agent-platform/internal/connector"
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
)

func TestCanceledDispatchedMCPCallIsUnknownWithoutReplay(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "book_meeting", nil)
	defer upstream.Close()
	arrived := make(chan struct{}, 1)
	var count atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		json.Unmarshal(body, &request)
		if request.Method == "tools/call" {
			count.Add(1)
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		upstream.Config.Handler.ServeHTTP(w, r)
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.CallTool(ctx, "demo", "book_meeting", nil, nil); done <- err }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("tool call did not dispatch")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, connector.ErrOutcomeUnknown) {
			t.Fatal("lost outcome uncertainty", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled tool did not stop")
	}
	if count.Load() != 1 {
		t.Fatal("write request automatically replayed", count.Load())
	}
}
