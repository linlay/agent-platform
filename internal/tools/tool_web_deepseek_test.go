package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

const deepSeekSearchSuccessJSON = `{"results":[{"rerank_score":2.4,"title":"来源标题","url":"https://example.com/article","snippet":"短摘要","summary":"完整摘要","content":"","site_name":null,"site_icon":null,"date":null,"authority":-1}]}`

func deepSeekWebTestConfig() config.Config {
	return config.Config{
		WebSearch: config.WebSearchConfig{Enabled: true, ProviderKey: "test", Timeout: 2, MaxResponseBytes: 1 << 20},
		WebFetch: config.WebFetchConfig{
			Enabled: true, DefaultProfile: "general",
			Profiles: map[string]config.WebFetchProfileConfig{
				"general": {Backend: "deepseek", ProviderKey: "test", ModelKey: "web-model", Timeout: 2, FetchTimeout: 2, MaxURLLength: 8192, MaxResponseBytes: 1 << 20, MaxMarkdownChars: 100000, MaxOutputTokens: 1200},
			},
		},
	}
}

func deepSeekWebTestExecutor(t *testing.T, server *httptest.Server) *RuntimeToolExecutor {
	t.Helper()
	executor, err := NewRuntimeToolExecutor(deepSeekWebTestConfig(), nil, nil, nil)
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	executor.WithModelRegistry(writeWebFetchRegistry(t, server.URL, "OPENAI"))
	executor.httpClient = server.Client()
	return executor
}

func invokeDeepSeekWebTest(t *testing.T, executor *RuntimeToolExecutor, ctx context.Context, tool string, args map[string]any) contracts.ToolExecutionResult {
	t.Helper()
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ToolNames: []string{tool}, ToolSetFrozen: true}}
	result, err := executor.Invoke(ctx, tool, args, execCtx)
	if err != nil {
		t.Fatalf("Invoke(%s): %v", tool, err)
	}
	return result
}

func deepSeekResultJSON(t *testing.T, result contracts.ToolExecutionResult) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(result.Structured)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	return value
}

func assertDeepSeekRequest(t *testing.T, r *http.Request, path string) map[string]any {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != path || r.URL.RawQuery != "" {
		t.Errorf("expected POST %s without query, got %s %s", path, r.Method, r.URL.String())
	}
	if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
		t.Errorf("expected bearer authorization and JSON headers")
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode API body: %v", err)
	}
	return body
}

func TestDeepSeekWebToolCatalogAndMountedInvoke(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body := assertDeepSeekRequest(t, r, "/v1/tools/search")
		if !reflect.DeepEqual(body, map[string]any{"query": "DeepSeek site:github.com after:2026"}) {
			t.Errorf("defaults must be omitted and search operators preserved: %#v", body)
		}
		_, _ = w.Write([]byte(deepSeekSearchSuccessJSON))
	}))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	definitions := map[string]map[string]any{}
	for _, definition := range executor.Definitions() {
		definitions[definition.Name] = definition.Parameters
	}
	for _, name := range []string{"web_search", "web_fetch"} {
		if definitions[name] == nil {
			t.Fatalf("missing embedded tool %s", name)
		}
	}
	properties := contracts.AnyMapNode(definitions["web_search"]["properties"])
	for _, field := range []string{"query", "count", "originalQuestion", "localization", "needContent"} {
		if properties[field] == nil {
			t.Errorf("web_search schema is missing %s", field)
		}
	}
	result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", map[string]any{"query": "DeepSeek site:github.com after:2026"})
	if result.Error != "" || result.Structured["ok"] != true || requests.Load() != 1 || !strings.Contains(result.Output, "https://example.com/article") {
		t.Fatalf("registered tool did not return a source: %#v, requests=%d", result, requests.Load())
	}
	denied, err := executor.Invoke(context.Background(), "web_search", map[string]any{"query": "test"}, &contracts.ExecutionContext{Session: contracts.QuerySession{ToolSetFrozen: true}})
	if err != nil || denied.Error != "tool_not_mounted" || requests.Load() != 1 {
		t.Fatalf("unmounted tool must not reach API: %#v, %v", denied, err)
	}
	executor.cfg.WebSearch.Enabled = false
	disabled := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", map[string]any{"query": "test"})
	if disabled.Error != "web_search_disabled" || requests.Load() != 1 {
		t.Fatalf("disabled tool must not reach API: %#v", disabled)
	}
}

func TestDeepSeekWebSearchOptionalInputsAndUnicode(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{"all options", map[string]any{"query": "实时消息", "count": 50, "originalQuestion": "用户的问题", "localization": "203.0.113.8", "needContent": true}, map[string]any{"query": "实时消息", "count": float64(50), "ori_question": "用户的问题", "localization": "203.0.113.8", "need_content": true}},
		{"unicode boundary", map[string]any{"query": strings.Repeat("🧑", 128), "originalQuestion": strings.Repeat("文", 128)}, map[string]any{"query": strings.Repeat("🧑", 128), "ori_question": strings.Repeat("文", 128)}},
		{"nullable question", map[string]any{"query": "query", "originalQuestion": nil}, map[string]any{"query": "query"}},
		{"JSON integer", map[string]any{"query": "query", "count": float64(1)}, map[string]any{"query": "query", "count": float64(1)}},
		{"json number", map[string]any{"query": "query", "count": json.Number("2")}, map[string]any{"query": "query", "count": float64(2)}},
		{"boolean true string", map[string]any{"query": "query", "needContent": "true"}, map[string]any{"query": "query", "need_content": true}},
		{"boolean false string", map[string]any{"query": "query", "needContent": "false", "localization": ""}, map[string]any{"query": "query", "need_content": false, "localization": ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := assertDeepSeekRequest(t, r, "/v1/tools/search")
				if !reflect.DeepEqual(body, test.want) {
					t.Errorf("wrong API input: got %#v, want %#v", body, test.want)
				}
				_, _ = w.Write([]byte(deepSeekSearchSuccessJSON))
			}))
			defer server.Close()
			result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", test.args)
			if result.Error != "" || result.Structured["ok"] != true {
				t.Fatalf("expected success, got %#v", result)
			}
			decoded := deepSeekResultJSON(t, result)
			items, ok := decoded["results"].([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("expected one result, got %#v", decoded)
			}
			item := items[0].(map[string]any)
			for _, name := range []string{"site_name", "site_icon", "date"} {
				if value, present := item[name]; !present || value != nil {
					t.Errorf("nullable metadata %s must remain null: %#v", name, item)
				}
			}
			if item["content"] != "" || item["authority"] != float64(-1) || item["rerank_score"] != 2.4 || item["summary"] != "完整摘要" {
				t.Errorf("search evidence fields were lost: %#v", item)
			}
		})
	}
}

func TestDeepSeekWebSearchInvalidInputsNeverCallAPI(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	invalid := []map[string]any{
		{}, {"query": nil}, {"query": ""}, {"query": " \n\t\u3000"}, {"query": 123}, {"query": strings.Repeat("🧑", 129)},
		{"query": "q", "count": 0}, {"query": "q", "count": 51}, {"query": "q", "count": 1.5}, {"query": "q", "count": "2"}, {"query": "q", "count": nil}, {"query": "q", "count": true}, {"query": "q", "count": math.NaN()},
		{"query": "q", "originalQuestion": ""}, {"query": "q", "originalQuestion": strings.Repeat("问", 129)}, {"query": "q", "originalQuestion": 1},
		{"query": "q", "localization": nil}, {"query": "q", "localization": 1}, {"query": "q", "needContent": "TRUE"}, {"query": "q", "needContent": " true"}, {"query": "q", "needContent": 1}, {"query": "q", "needContent": nil},
		{"query": "q", "apiKey": "do-not-forward"},
	}
	for index, args := range invalid {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", args)
			if result.Error != "web_search_input_invalid" || result.Structured["ok"] != false || result.ExitCode == 0 {
				t.Fatalf("expected invalid input, got %#v", result)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input made %d API requests", calls.Load())
	}
}

func TestDeepSeekWebSearchEmptyAndInvalidResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, wantError string
	}{
		{"no matches", `{"results":[]}`, ""},
		{"null array", `{"results":null}`, "web_search_response_invalid"},
		{"missing array", `{}`, "web_search_response_invalid"},
		{"object array", `{"results":{}}`, "web_search_response_invalid"},
		{"malformed JSON", `{"results":`, "web_search_response_invalid"},
		{"trailing JSON", `{"results":[]} {}`, "web_search_response_invalid"},
		{"invalid source", `{"results":[{"url":"javascript:alert(1)"}]}`, "web_search_response_invalid"},
		{"credential source", `{"results":[{"url":"https://user:pass@example.com/"}]}`, "web_search_response_invalid"},
		{"wrong field type", `{"results":[{"url":"https://example.com/","summary":{}}]}`, "web_search_response_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", map[string]any{"query": "query"})
			if result.Error != test.wantError {
				t.Fatalf("expected %q, got %#v", test.wantError, result)
			}
			if test.wantError == "" {
				items, ok := deepSeekResultJSON(t, result)["results"].([]any)
				if !ok || len(items) != 0 || result.Structured["ok"] != true {
					t.Fatalf("no matches must be a successful empty array: %#v", result)
				}
			}
		})
	}
}

func TestDeepSeekWebSearchRequiredResponseFields(t *testing.T) {
	var fixture struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(deepSeekSearchSuccessJSON), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"rerank_score", "title", "url", "snippet", "summary", "content", "site_name", "site_icon", "date", "authority"} {
		for _, missing := range []bool{true, false} {
			// The three nullable fields must be present, but explicit null is valid.
			if !missing && (field == "site_name" || field == "site_icon" || field == "date") {
				continue
			}
			t.Run(fmt.Sprintf("%s/missing=%t", field, missing), func(t *testing.T) {
				item := make(map[string]any, len(fixture.Results[0]))
				for name, value := range fixture.Results[0] {
					item[name] = value
				}
				if missing {
					delete(item, field)
				} else {
					item[field] = nil
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{item}})
				}))
				defer server.Close()
				result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", map[string]any{"query": "query"})
				if result.Error != "web_search_response_invalid" {
					t.Fatalf("missing/null required field %s manufactured evidence: %#v", field, result)
				}
			})
		}
	}
}

func TestDeepSeekWebSearchLargeContentHasBoundedModelOutput(t *testing.T) {
	const source = "https://example.com/article"
	content := strings.Repeat("正文🧑", contracts.ToolModelOutputMaxBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := assertDeepSeekRequest(t, r, "/v1/tools/search")
		if body["need_content"] != true {
			t.Errorf("expected need_content=true upstream, got %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{
			"rerank_score": 2.4, "title": "完整证据", "url": source, "snippet": "短摘要", "summary": "完整摘要", "content": content, "site_name": nil, "site_icon": nil, "date": nil, "authority": -1,
		}}})
	}))
	defer server.Close()
	result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", map[string]any{"query": "query", "needContent": true})
	if result.Error != "" || len(result.Output) > contracts.ToolModelOutputMaxBytes || !utf8.ValidString(result.Output) || strings.ContainsRune(result.Output, utf8.RuneError) || !strings.Contains(result.Output, source) {
		t.Fatalf("model output must be bounded with a valid source and UTF-8: error=%s, outputBytes=%d", result.Error, len(result.Output))
	}
	if !strings.Contains(strings.ToLower(result.Output), "truncat") {
		t.Fatal("model output must disclose omitted search evidence")
	}
	structured := deepSeekResultJSON(t, result)
	if got := structured["results"].([]any)[0].(map[string]any)["content"]; got != content {
		t.Fatal("structured evidence must preserve the complete original content for large-result readback")
	}
}

func TestDeepSeekWebFetchRawMarkdownNeedsOnlyProvider(t *testing.T) {
	const markdown = "# 原文\n\n正文与 [来源](https://example.com/reference)。\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := assertDeepSeekRequest(t, r, "/v1/tools/fetch")
		if !reflect.DeepEqual(body, map[string]any{"url": "http://example.com/article"}) {
			t.Errorf("Fetch must preserve the supplied URL and send no model fields: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "", "content": markdown})
	}))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	// A provider-only registry proves that raw Fetch does not resolve a model.
	executor.models = writeDeepSeekProviderOnlyRegistry(t, server.URL, "test-key")
	result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_fetch", map[string]any{"url": "http://example.com/article"})
	if result.Error != "" || result.Structured["result"] != markdown || result.Structured["title"] != "" || result.Structured["backend"] != "deepseek" || result.Structured["directReturn"] != true {
		t.Fatalf("raw Markdown/empty title should succeed without a model: %#v", result)
	}
	if _, exists := result.Structured["modelKey"]; exists {
		t.Errorf("raw Fetch must not claim a model call: %#v", result.Structured)
	}
	if _, exists := result.Structured["finalUrl"]; exists {
		t.Errorf("DeepSeek does not supply a final redirect URL: %#v", result.Structured)
	}
}

func TestDeepSeekWebFetchPromptReturnsMarkdownWithoutSummaryModel(t *testing.T) {
	var fetchCalls, modelCalls atomic.Int32
	const markdown = "# 原文\n\n待总结正文。"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/fetch" {
			modelCalls.Add(1)
			t.Errorf("DeepSeek Fetch must not invoke another model: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fetchCalls.Add(1)
		assertDeepSeekRequest(t, r, "/v1/tools/fetch")
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "页面标题", "content": markdown})
	}))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	// The runtime has no summary model: even a supplied prompt must still work.
	executor.models = writeDeepSeekProviderOnlyRegistry(t, server.URL, "test-key")
	for _, preapproved := range []bool{false, true} {
		t.Run(fmt.Sprintf("preapproved=%t", preapproved), func(t *testing.T) {
			fetchCalls.Store(0)
			modelCalls.Store(0)
			executor.cfg.WebFetch.PreapprovedHosts = nil
			if preapproved {
				executor.cfg.WebFetch.PreapprovedHosts = []string{"example.com"}
			}
			result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_fetch", map[string]any{"url": "https://example.com/article", "prompt": "请总结关键信息"})
			if result.Error != "" || result.Structured["result"] != markdown || result.Structured["extractionPrompt"] != "请总结关键信息" || result.Structured["directReturn"] != true || fetchCalls.Load() != 1 || modelCalls.Load() != 0 {
				t.Fatalf("expected Markdown and extraction goal without another model request: %#v, fetch=%d, model=%d", result, fetchCalls.Load(), modelCalls.Load())
			}
			if _, exists := result.Structured["modelKey"]; exists {
				t.Fatal("raw Fetch must not claim a summary model call")
			}
		})
	}
}

func TestDeepSeekWebFetchInvalidURLsNeverCallAPI(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	for _, address := range []string{"https://user:pass@example.com/", "file:///tmp/private", "https:///missing-host", "https://127.0.0.1/private", "https://localhost/private", "https://example.com/" + strings.Repeat("a", 8192)} {
		result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_fetch", map[string]any{"url": address})
		if result.Error != "web_fetch_url_invalid" || result.Structured["ok"] != false {
			t.Errorf("invalid Fetch URL accepted: %q, %#v", address, result)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid URLs made %d API requests", calls.Load())
	}
}

func TestDeepSeekWebFetchInvalidResponseAndTruncation(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"title":null,"content":"text"}`, `{"title":"","content":null}`, `{"title":"","content":"  "}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_fetch", map[string]any{"url": "https://example.com/article"})
			if result.Error != "web_fetch_response_invalid" {
				t.Fatalf("expected invalid Fetch response, got %#v", result)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "", "content": strings.Repeat("新闻", 200)})
	}))
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	profile := executor.cfg.WebFetch.Profiles["general"]
	profile.MaxMarkdownChars = 101
	executor.cfg.WebFetch.Profiles["general"] = profile
	result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_fetch", map[string]any{"url": "https://example.com/article"})
	var preview map[string]any
	if err := json.Unmarshal([]byte(result.Output), &preview); err != nil {
		t.Fatal(err)
	}
	text := contracts.AnyStringNode(preview["result"])
	if result.Error != "" || result.Structured["result"] != strings.Repeat("新闻", 200) || preview["contentTruncatedForLLM"] != true || len(text) > 101 || !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) {
		t.Fatalf("the model preview must be bounded while Structured preserves the original Markdown: %#v", result)
	}
}

func TestDeepSeekWebHTTPFailureClassificationAndBoundedRetry(t *testing.T) {
	for _, tool := range []string{"web_search", "web_fetch"} {
		for _, status := range []int{400, 401, 402, 403, 404, 408, 413, 429, 500, 504} {
			t.Run(fmt.Sprintf("%s/%d", tool, status), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"reflected Bearer test-key","type":"upstream_error","param":null,"code":"upstream_error"}}`))
				}))
				defer server.Close()
				args := map[string]any{"query": "query"}
				if tool == "web_fetch" {
					args = map[string]any{"url": "https://example.com/article"}
				}
				result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), tool, args)
				retryable := status == 408 || status == 429 || status >= 500
				wantCalls := int32(1)
				if retryable {
					wantCalls = 3
				}
				if result.Error != tool+"_http_error" || result.Structured["httpStatus"] != status || result.Structured["retryable"] != retryable || calls.Load() != wantCalls {
					t.Fatalf("wrong error/retry policy: %#v, calls=%d want=%d", result, calls.Load(), wantCalls)
				}
				if strings.Contains(result.Output, "test-key") || strings.Contains(result.Output, "reflected") || strings.Contains(result.Output, "upstream_error") {
					t.Fatalf("upstream error text must not reflect secrets: %s", result.Output)
				}
			})
		}
	}
}

func TestDeepSeekWebTransientFailureRecovers(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", map[string]any{"query": "query"})
	if result.Error != "" || calls.Load() != 3 {
		t.Fatalf("temporary HTTP failure should recover within retry bound: %#v, calls=%d", result, calls.Load())
	}
}

func TestDeepSeekWebCancellationInterruptsRetryAndTimeout(t *testing.T) {
	t.Run("cancel backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			cancel()
		}))
		defer server.Close()
		start := time.Now()
		result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), ctx, "web_search", map[string]any{"query": "query"})
		if result.Error != "web_search_request_failed" || result.Structured["retryable"] != false || calls.Load() != 1 || time.Since(start) > time.Second {
			t.Fatalf("cancellation did not interrupt request/backoff: %#v, calls=%d", result, calls.Load())
		}
	})
	t.Run("parent deadline", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		start := time.Now()
		result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), ctx, "web_fetch", map[string]any{"url": "https://example.com/article"})
		if result.Error != "web_fetch_request_failed" || time.Since(start) > time.Second {
			t.Fatalf("parent deadline must bound Fetch: %#v", result)
		}
	})
	t.Run("configured timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		executor := deepSeekWebTestExecutor(t, server)
		executor.cfg.WebSearch.Timeout = 1
		start := time.Now()
		result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", map[string]any{"query": "query"})
		if result.Error != "web_search_request_failed" || time.Since(start) > 3*time.Second {
			t.Fatalf("configured timeout must bound Search: %#v", result)
		}
	})
}

func TestDeepSeekWebResponseLimitAndRedirectProtectCredentials(t *testing.T) {
	for _, tool := range []string{"web_search", "web_fetch"} {
		t.Run(tool+"/limit", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 1025))) }))
			defer server.Close()
			executor := deepSeekWebTestExecutor(t, server)
			executor.cfg.WebSearch.MaxResponseBytes = 1024
			profile := executor.cfg.WebFetch.Profiles["general"]
			profile.MaxResponseBytes = 1024
			executor.cfg.WebFetch.Profiles["general"] = profile
			args := map[string]any{"query": "query"}
			if tool == "web_fetch" {
				args = map[string]any{"url": "https://example.com/article"}
			}
			result := invokeDeepSeekWebTest(t, executor, context.Background(), tool, args)
			if result.Error != tool+"_response_too_large" || result.Structured["retryable"] != false {
				t.Fatalf("response limit not enforced: %#v", result)
			}
		})
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s/redirect/%d", tool, status), func(t *testing.T) {
				var targetCalls atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
				defer target.Close()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("API origin did not receive configured authorization")
					}
					http.Redirect(w, r, target.URL+"/credential-sink?secret=test-key", status)
				}))
				defer server.Close()
				args := map[string]any{"query": "query"}
				if tool == "web_fetch" {
					args = map[string]any{"url": "https://example.com/article"}
				}
				result := invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), tool, args)
				if result.Error != tool+"_redirect_rejected" || targetCalls.Load() != 0 || strings.Contains(result.Output, "test-key") || strings.Contains(result.Output, "credential-sink") {
					t.Fatalf("API redirect leaked request or sensitive Location: %#v, targetCalls=%d", result, targetCalls.Load())
				}
			})
		}
	}
}

type deepSeekWebRoundTripFunc func(*http.Request) (*http.Response, error)

func (f deepSeekWebRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepSeekWebTransportErrorDoesNotEchoSecrets(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	executor := deepSeekWebTestExecutor(t, server)
	executor.httpClient = &http.Client{Transport: deepSeekWebRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxy https://user:test-key@proxy.example/ failed")
	})}
	result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", map[string]any{"query": "query"})
	if result.Error != "web_search_request_failed" || strings.Contains(result.Output, "test-key") || strings.Contains(result.Output, "proxy.example") {
		t.Fatalf("transport error leaked private proxy details: %#v", result)
	}
}

func writeDeepSeekProviderOnlyRegistry(t *testing.T, baseURL, key string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("key: test\nbaseUrl: %q\napiKey: %q\n", baseURL, key)
	if err := os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load provider-only registry: %v", err)
	}
	return registry
}

func TestDeepSeekWebSeparateProviderKeysAndRotation(t *testing.T) {
	var searchKey atomic.Value
	searchKey.Store("search-key-one")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer fetch-key"
		if r.URL.Path == "/v1/tools/search" {
			expected = "Bearer " + searchKey.Load().(string)
		}
		if r.Header.Get("Authorization") != expected {
			t.Error("request did not use its configured provider credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/tools/search":
			_, _ = w.Write([]byte(deepSeekSearchSuccessJSON))
		case "/v1/tools/fetch":
			_, _ = w.Write([]byte(`{"title":"Page","content":"Markdown content"}`))
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0700); err != nil {
		t.Fatal(err)
	}
	writeProvider := func(name, key string) {
		t.Helper()
		data := fmt.Sprintf("key: %s\nbaseUrl: %q\napiKey: %q\n", name, server.URL, key)
		if err := os.WriteFile(filepath.Join(root, "providers", name+".yml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeProvider("search-api", "search-key-one")
	writeProvider("fetch-api", "fetch-key")
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := deepSeekWebTestConfig()
	cfg.WebSearch.ProviderKey = "search-api"
	profile := cfg.WebFetch.Profiles["general"]
	profile.ProviderKey = "fetch-api"
	cfg.WebFetch.Profiles["general"] = profile
	executor, err := NewRuntimeToolExecutor(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor.WithModelRegistry(registry)
	executor.httpClient = server.Client()
	checkBoth := func() {
		t.Helper()
		for _, call := range []struct {
			name string
			args map[string]any
		}{
			{"web_search", map[string]any{"query": "public information"}},
			{"web_fetch", map[string]any{"url": "https://example.com/article", "prompt": "extract facts"}},
		} {
			result := invokeDeepSeekWebTest(t, executor, context.Background(), call.name, call.args)
			if result.Error != "" || result.Structured["ok"] != true {
				t.Fatalf("configured %s failed: %#v", call.name, result)
			}
		}
	}
	checkBoth()
	writeProvider("search-api", "search-key-two")
	searchKey.Store("search-key-two")
	if err := registry.ReloadProviders(); err != nil {
		t.Fatal(err)
	}
	// Only Search rotated: Fetch must retain its own configured Key.
	checkBoth()
}

func TestDeepSeekWebMissingProviderConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid provider config must not send HTTP") }))
	defer server.Close()
	for _, test := range []struct {
		name, providerKey, want string
		registry                *models.ModelRegistry
	}{
		{"missing registry", "test", "web_search_provider_unavailable", nil},
		{"missing provider key", "", "web_search_provider_config_invalid", writeWebFetchRegistry(t, server.URL, "OPENAI")},
		{"unknown provider", "unknown", "web_search_provider_not_found", writeWebFetchRegistry(t, server.URL, "OPENAI")},
		{"empty API key", "test", "web_search_provider_config_invalid", writeDeepSeekProviderOnlyRegistry(t, server.URL, "")},
		{"invalid base URL", "test", "web_search_provider_config_invalid", writeDeepSeekProviderOnlyRegistry(t, "https://user:test-key@example.com/", "test-key")},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := deepSeekWebTestExecutor(t, server)
			executor.cfg.WebSearch.ProviderKey = test.providerKey
			executor.models = test.registry
			result := invokeDeepSeekWebTest(t, executor, context.Background(), "web_search", map[string]any{"query": "query"})
			if result.Error != test.want || strings.Contains(result.Output, "test-key") {
				t.Fatalf("expected safe %s, got %#v", test.want, result)
			}
		})
	}
}

func TestDeepSeekWebEndpointBaseURLPaths(t *testing.T) {
	for _, test := range []struct{ base, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/v1/tools/search"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/v1/tools/search"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/tools/search"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com/v1/tools/search"},
		{"https://gateway.example/deepseek", "https://gateway.example/deepseek/v1/tools/search"},
		{"https://gateway.example/deepseek/v1/", "https://gateway.example/deepseek/v1/tools/search"},
	} {
		got, err := deepSeekWebEndpoint(test.base, "search")
		if err != nil || got != test.want {
			t.Errorf("endpoint(%q) = %q, %v; want %q", test.base, got, err, test.want)
		}
	}
	for _, base := range []string{"", "ftp://example.com", "https:///missing", "https://user:password@example.com", "https://example.com?key=secret", "https://example.com/#fragment"} {
		if got, err := deepSeekWebEndpoint(base, "fetch"); err == nil {
			t.Errorf("invalid provider URL accepted: %q -> %q", base, got)
		}
	}
}
