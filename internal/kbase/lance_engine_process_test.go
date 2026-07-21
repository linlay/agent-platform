package kbase

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReadLanceHandshakeParsesSingleReadyLine(t *testing.T) {
	want := LanceEngineHandshake{
		ProtocolVersion: 1,
		EngineVersion:   "1.0.0",
		LanceDBVersion:  "0.30.0",
		ListenAddress:   "127.0.0.1:54321",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	got, err := readLanceHandshake(context.Background(), strings.NewReader(string(data)+"\nignored log line\n"))
	if err != nil {
		t.Fatalf("readLanceHandshake: %v", err)
	}
	if got != want {
		t.Fatalf("handshake = %#v, want %#v", got, want)
	}
}

func TestLanceEngineClientLeavesTimeoutToEachOperation(t *testing.T) {
	process := NewLanceEngineProcess(nil)
	if process.client.Timeout != 0 {
		t.Fatalf("shared HTTP client timeout = %s, want zero so operation contexts control deadlines", process.client.Timeout)
	}
}

func TestReadLanceHandshakeRejectsInvalidReadyLine(t *testing.T) {
	_, err := readLanceHandshake(context.Background(), strings.NewReader("not-json\n"))
	if err == nil || !strings.Contains(err.Error(), "parse lance engine handshake") {
		t.Fatalf("readLanceHandshake error = %v", err)
	}
}

func TestRandomEngineTokenIs256BitsHexEncoded(t *testing.T) {
	first, err := randomEngineToken()
	if err != nil {
		t.Fatalf("randomEngineToken: %v", err)
	}
	second, err := randomEngineToken()
	if err != nil {
		t.Fatalf("randomEngineToken second call: %v", err)
	}
	if first == second {
		t.Fatal("two independently generated engine tokens are equal")
	}
	decoded, err := hex.DecodeString(first)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded token length = %d, want 32", len(decoded))
	}
}

func TestLanceEngineHealthUsesBearerTokenAndRecordsHandshake(t *testing.T) {
	const token = "local-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/health" || request.Method != http.MethodGet {
			t.Errorf("request = %s %s, want GET /v1/health", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(LanceEngineHandshake{
			ProtocolVersion: 2,
			EngineVersion:   "1.2.3",
			LanceDBVersion:  "0.30.0",
			ListenAddress:   "127.0.0.1:12345",
		})
	}))
	defer server.Close()

	process := testRunningLanceProcess(server, token)
	if err := process.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	state := process.State()
	if !state.Available || state.ProtocolVersion != 2 || state.EngineVersion != "1.2.3" || state.LanceDBVersion != "0.30.0" {
		t.Fatalf("state = %#v", state)
	}
}

func TestLanceEngineClientMapsStableErrorPayload(t *testing.T) {
	const token = "local-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(lanceErrorResponse{Code: "storage_busy", Message: "generation is locked"})
	}))
	defer server.Close()

	process := testRunningLanceProcess(server, token)
	err := process.doJSON(context.Background(), http.MethodPost, "/v1/search", map[string]string{"requestId": "request-1"}, nil, time.Second)
	var engineErr *LanceEngineError
	if !errors.As(err, &engineErr) {
		t.Fatalf("doJSON error = %T %v, want *LanceEngineError", err, err)
	}
	if engineErr.Status != http.StatusConflict || engineErr.Code != "storage_busy" || engineErr.Message != "generation is locked" {
		t.Fatalf("mapped error = %#v", engineErr)
	}
	if got := engineErr.Error(); got != "storage_busy: generation is locked" {
		t.Fatalf("error string = %q", got)
	}
}

func TestLanceEngineClientMapsNestedSidecarErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"storage_corrupt","message":"invalid manifest"}}`))
	}))
	defer server.Close()
	process := testRunningLanceProcess(server, "token")
	err := process.doJSON(context.Background(), http.MethodPost, "/v1/search", map[string]string{}, nil, time.Second)
	var engineErr *LanceEngineError
	if !errors.As(err, &engineErr) || engineErr.Code != "storage_corrupt" || KindOf(err) != ErrorUnavailable {
		t.Fatalf("nested error = %#v kind=%s", err, KindOf(err))
	}
}

func testRunningLanceProcess(server *httptest.Server, token string) *LanceEngineProcess {
	return &LanceEngineProcess{
		cmd:     &exec.Cmd{},
		baseURL: server.URL,
		token:   token,
		client:  server.Client(),
	}
}
