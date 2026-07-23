package kbase

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/supportpkg"
)

func TestLanceEngineResolveExecutableUsesSupportPackage(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "kbase-support")
	binaryName := lanceEngineExecutableName
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(pluginDir, "bin", binaryName)
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatalf("mkdir support package: %v", err)
	}
	if err := os.WriteFile(binaryPath, []byte("test executable"), 0o755); err != nil {
		t.Fatalf("write support executable: %v", err)
	}
	manifest, err := json.Marshal(map[string]any{
		"kind":    "support-package",
		"id":      "kbase-support",
		"version": "v1.0.0",
		"platform": map[string]string{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		"executables": map[string]string{
			lanceEngineExecutableName: filepath.Join("bin", binaryName),
		},
	})
	if err != nil {
		t.Fatalf("marshal support manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, supportpkg.ManifestName), manifest, 0o644); err != nil {
		t.Fatalf("write support manifest: %v", err)
	}
	registry, loadErrs := supportpkg.LoadDir(root, supportpkg.Target{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if len(loadErrs) != 0 {
		t.Fatalf("load support package: %v", loadErrs)
	}

	got, err := NewLanceEngineProcess(registry).resolveExecutable()
	if err != nil {
		t.Fatalf("resolve executable: %v", err)
	}
	if got != binaryPath {
		t.Fatalf("resolved executable = %q, want %q", got, binaryPath)
	}
}

func TestLanceEngineResolveExecutableIgnoresRetiredSingleFileEnvironmentOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-lance-engine")
	if err := os.WriteFile(override, []byte("test executable"), 0o755); err != nil {
		t.Fatalf("write override executable: %v", err)
	}
	t.Setenv("AP_KBASE_"+"LANCE_ENGINE", override)

	got, err := NewLanceEngineProcess(nil).resolveExecutable()
	if got != "" {
		t.Fatalf("resolved retired environment override: %q", got)
	}
	if err == nil || KindOf(err) != ErrorUnavailable {
		t.Fatalf("resolve error = %v, want unavailable", err)
	}
}

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
