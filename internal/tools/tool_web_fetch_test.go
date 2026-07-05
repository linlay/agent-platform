package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestWebFetchDisabled(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "web_fetch_disabled" {
		t.Fatalf("expected disabled error, got %#v", result)
	}
}

func TestWebFetchRejectsLocalhostURL(t *testing.T) {
	registry := writeWebFetchRegistry(t, "http://127.0.0.1:1", "OPENAI")
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, nil)
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://localhost/page",
		"prompt": "summarize",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "web_fetch_request_failed" || !strings.Contains(contracts.AnyStringNode(result.Structured["message"]), "blocked host") {
		t.Fatalf("expected blocked host error, got %#v", result)
	}
}

func TestWebFetchMissingModel(t *testing.T) {
	registry := writeWebFetchRegistry(t, "http://127.0.0.1:1", "OPENAI")
	cfg := defaultWebFetchTestConfig()
	cfg.Profiles["general"] = config.WebFetchProfileConfig{ModelKey: "missing-model"}
	executor := webFetchTestExecutor(cfg, registry, nil)
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/page",
		"prompt": "summarize",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "web_fetch_model_not_found" {
		t.Fatalf("expected missing model error, got %#v", result)
	}
}

func TestWebFetchHTMLAppliesPromptWithSmallModel(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Title</h1><p>Hello <a href="/next">link</a>.</p></body></html>`))
	}))
	defer webServer.Close()

	var captured map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary result"}}],"usage":{"total_tokens":9}}`))
	}))
	defer modelServer.Close()

	registry := writeWebFetchRegistry(t, modelServer.URL, "OPENAI")
	client := routedHTTPClient(map[string]string{"example.com": webServer.URL})
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, client)
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/page",
		"prompt": "summarize the title",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "" || result.Structured["result"] != "summary result" {
		t.Fatalf("expected summary result, got %#v", result)
	}
	messages, _ := captured["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("expected OpenAI messages, got %#v", captured)
	}
	userMessage, _ := messages[1].(map[string]any)
	userPrompt := contracts.AnyStringNode(userMessage["content"])
	if !strings.Contains(userPrompt, "# Title") || !strings.Contains(userPrompt, "Hello link.") || strings.Contains(userPrompt, "/next") {
		t.Fatalf("expected markdown-ish content in model prompt, got %q", userPrompt)
	}
}

func TestWebFetchPreapprovedMarkdownDirectReturn(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Direct docs\n\nHello."))
	}))
	defer webServer.Close()

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("model should not be called for preapproved markdown direct return")
	}))
	defer modelServer.Close()

	registry := writeWebFetchRegistry(t, modelServer.URL, "OPENAI")
	cfg := defaultWebFetchTestConfig()
	cfg.PreapprovedHosts = []string{"example.com"}
	client := routedHTTPClient(map[string]string{"example.com": webServer.URL})
	executor := webFetchTestExecutor(cfg, registry, client)
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/docs",
		"prompt": "return content",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "" || result.Structured["result"] != "# Direct docs\n\nHello." || result.Structured["directReturn"] != true {
		t.Fatalf("expected direct markdown result, got %#v", result)
	}
}

func TestWebFetchSameHostRedirectFollows(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("final content"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer webServer.Close()

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("model should not be called for preapproved markdown direct return")
	}))
	defer modelServer.Close()

	registry := writeWebFetchRegistry(t, modelServer.URL, "OPENAI")
	cfg := defaultWebFetchTestConfig()
	cfg.PreapprovedHosts = []string{"example.com"}
	executor := webFetchTestExecutor(cfg, registry, routedHTTPClient(map[string]string{"example.com": webServer.URL}))
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/start",
		"prompt": "return content",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "" || result.Structured["finalUrl"] != "https://example.com/final" || result.Structured["result"] != "final content" {
		t.Fatalf("expected followed redirect result, got %#v", result)
	}
}

func TestWebFetchCrossHostRedirectReturnsInstruction(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.com/final", http.StatusFound)
	}))
	defer webServer.Close()

	registry := writeWebFetchRegistry(t, "http://127.0.0.1:1", "OPENAI")
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, routedHTTPClient(map[string]string{"example.com": webServer.URL}))
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/start",
		"prompt": "summarize",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "" || result.Structured["redirect"] == nil || !strings.Contains(contracts.AnyStringNode(result.Structured["result"]), "REDIRECT DETECTED") {
		t.Fatalf("expected redirect instruction, got %#v", result)
	}
}

func TestWebFetchBinaryPersistsAndAppliesPrompt(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7\n/Title (Demo)\n"))
	}))
	defer webServer.Close()

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pdf summary"}}]}`))
	}))
	defer modelServer.Close()

	registry := writeWebFetchRegistry(t, modelServer.URL, "OPENAI")
	chatDir := t.TempDir()
	execCtx := &contracts.ExecutionContext{
		Session: contracts.QuerySession{
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, routedHTTPClient(map[string]string{"example.com": webServer.URL}))
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/file.pdf",
		"prompt": "summarize",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	path := contracts.AnyStringNode(result.Structured["persistedPath"])
	if result.Error != "" || result.Structured["result"] != "pdf summary" || path == "" {
		t.Fatalf("expected persisted binary summary, got %#v", result)
	}
	if !strings.Contains(path, filepath.Join(chat.ToolRootDirName, "web-fetch")) {
		t.Fatalf("expected web-fetch tool path, got %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted binary file: %v", err)
	}
}

func TestWebFetchRejectsOversizedResponse(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	defer webServer.Close()

	registry := writeWebFetchRegistry(t, "http://127.0.0.1:1", "OPENAI")
	cfg := defaultWebFetchTestConfig()
	profile := cfg.Profiles["general"]
	profile.MaxResponseBytes = 8
	cfg.Profiles["general"] = profile
	executor := webFetchTestExecutor(cfg, registry, routedHTTPClient(map[string]string{"example.com": webServer.URL}))
	result, err := executor.invokeWebFetch(context.Background(), map[string]any{
		"url":    "https://example.com/large",
		"prompt": "summarize",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "web_fetch_request_failed" || !strings.Contains(contracts.AnyStringNode(result.Structured["message"]), "max-response-bytes") {
		t.Fatalf("expected oversized response error, got %#v", result)
	}
}

func TestWebFetchHonorsContextTimeout(t *testing.T) {
	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer webServer.Close()

	registry := writeWebFetchRegistry(t, "http://127.0.0.1:1", "OPENAI")
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, routedHTTPClient(map[string]string{"example.com": webServer.URL}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := executor.invokeWebFetch(ctx, map[string]any{
		"url":    "https://example.com/slow",
		"prompt": "summarize",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeWebFetch: %v", err)
	}
	if result.Error != "web_fetch_request_failed" {
		t.Fatalf("expected timeout request error, got %#v", result)
	}
}

func TestCompleteTextModelAnthropic(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Fatalf("unexpected api key: %q", r.Header.Get("X-Api-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"anthropic answer"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer server.Close()

	registry := writeWebFetchRegistry(t, server.URL, "ANTHROPIC")
	model, provider, err := registry.Get("web-model")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	executor := &RuntimeToolExecutor{httpClient: server.Client()}
	content, usage, err := executor.completeTextModel(context.Background(), model, provider, textModelRequest{
		SystemPrompt:    "system",
		UserPrompt:      "user",
		MaxOutputTokens: 99,
	})
	if err != nil {
		t.Fatalf("completeTextModel: %v", err)
	}
	if content != "anthropic answer" || contracts.AnyIntNode(usage["input_tokens"]) != 3 {
		t.Fatalf("unexpected content/usage: %q %#v", content, usage)
	}
	if captured["max_tokens"] != float64(99) {
		t.Fatalf("expected max_tokens 99, got %#v", captured)
	}
}

func defaultWebFetchTestConfig() config.WebFetchConfig {
	return config.WebFetchConfig{
		Enabled:        true,
		DefaultProfile: "general",
		Profiles: map[string]config.WebFetchProfileConfig{
			"general": {
				ModelKey:         "web-model",
				Timeout:          60,
				FetchTimeout:     60,
				MaxURLLength:     2000,
				MaxResponseBytes: 10 * 1024 * 1024,
				MaxMarkdownChars: 100000,
				MaxOutputTokens:  1200,
			},
		},
	}
}

func webFetchTestExecutor(cfg config.WebFetchConfig, registry *models.ModelRegistry, client *http.Client) *RuntimeToolExecutor {
	if len(cfg.Profiles) == 0 {
		cfg = defaultWebFetchTestConfig()
	}
	executor := &RuntimeToolExecutor{
		cfg:    config.Config{WebFetch: cfg},
		models: registry,
	}
	if client != nil {
		executor.httpClient = client
	}
	return executor
}

func writeWebFetchRegistry(t *testing.T, baseURL string, protocol string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	provider := strings.Join([]string{
		"key: test",
		"baseUrl: " + baseURL,
		"apiKey: test-key",
		"defaultModel: web-model",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte(provider), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	model := strings.Join([]string{
		"key: web-model",
		"provider: test",
		"protocol: " + protocol,
		"modelId: web-model-id",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "models", "web-model.yml"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}

type routeTransport struct {
	base   http.RoundTripper
	routes map[string]*neturl.URL
}

func routedHTTPClient(routes map[string]string) *http.Client {
	parsed := make(map[string]*neturl.URL, len(routes))
	for host, target := range routes {
		u, err := neturl.Parse(target)
		if err != nil {
			panic(err)
		}
		parsed[strings.ToLower(host)] = u
	}
	return &http.Client{Transport: routeTransport{base: http.DefaultTransport, routes: parsed}}
}

func (t routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if target := t.routes[strings.ToLower(req.URL.Hostname())]; target != nil {
		clone := req.Clone(req.Context())
		nextURL := *clone.URL
		nextURL.Scheme = target.Scheme
		nextURL.Host = target.Host
		clone.URL = &nextURL
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(req)
}
