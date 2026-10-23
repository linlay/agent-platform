package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

// The branch keeps DeepSeek's direct Markdown path while upstream providers
// may use a Desktop token instead of a static API key for prompted fetching.
func TestWebFetchPromptUsesRefreshedDesktopAccessToken(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>Example</h1>"))
	}))
	defer web.Close()
	wantToken := "test-token-a"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("unexpected credential header")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary"}}]}`))
	}))
	defer model.Close()
	root := t.TempDir()
	for _, dir := range []string{"providers", "models"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"providers/test.yml":   "key: test\nbaseUrl: " + model.URL + "\nuseAccessToken: true\ndefaultModel: web-model\n",
		"models/web-model.yml": "key: web-model\nprovider: test\nprotocol: OPENAI\nmodelId: web-model-id\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	identity := filepath.Join(root, "identity")
	registry, err := models.LoadModelRegistryWithIdentityFile(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	executor := webFetchTestExecutor(config.WebFetchConfig{}, registry, routedHTTPClient(map[string]string{"example.com": web.URL}))
	for _, token := range []string{"test-token-a", "test-token-b"} {
		wantToken = token
		if err := os.WriteFile(identity, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := executor.invokeWebFetch(context.Background(), map[string]any{"url": "https://example.com/page", "prompt": "summarize"}, &contracts.ExecutionContext{})
		if err != nil || result.Error != "" || result.Structured["result"] != "summary" {
			t.Fatalf("prompted fetch failed: err=%v result=%#v", err, result)
		}
	}
}
