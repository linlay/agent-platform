package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/contracts"
)

func deepSeekSearchPreviewSource(t *testing.T, sourceURL string) map[string]any {
	t.Helper()
	var response struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(deepSeekSearchSuccessJSON), &response); err != nil {
		t.Fatal(err)
	}
	source := response.Results[0]
	source["url"] = sourceURL
	source["title"] = "来源标题🧑"
	source["date"] = "2026-10-20T00:00:00Z"
	return source
}

func invokeDeepSeekSearchPreview(t *testing.T, sources []any) contracts.ToolExecutionResult {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertDeepSeekRequest(t, r, "/v1/tools/search")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": sources})
	}))
	defer server.Close()
	return invokeDeepSeekWebTest(t, deepSeekWebTestExecutor(t, server), context.Background(), "web_search", map[string]any{"query": "查找来源🧑"})
}

func decodeDeepSeekSearchPreview(t *testing.T, result contracts.ToolExecutionResult) map[string]any {
	t.Helper()
	if result.Error != "" || len(result.Output) > contracts.ToolModelOutputMaxChars || !utf8.ValidString(result.Output) || strings.ContainsRune(result.Output, utf8.RuneError) {
		t.Fatalf("expected a successful bounded UTF-8 preview: error=%q, outputBytes=%d", result.Error, len(result.Output))
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(result.Output), &preview); err != nil {
		t.Fatalf("model preview must remain valid JSON: %v", err)
	}
	return preview
}

func TestDeepSeekWebSearchPreviewSkipsOversizedMetadataAndSources(t *testing.T) {
	const firstURL = "https://example.com/first"
	const secondURL = "https://example.com/second"
	for _, name := range []string{"site icon", "source URL", "unknown nested metadata", "invalid date", "oversized fractional timestamp"} {
		t.Run(name, func(t *testing.T) {
			first := deepSeekSearchPreviewSource(t, firstURL)
			second := deepSeekSearchPreviewSource(t, secondURL)
			longText := strings.Repeat("x", contracts.ToolModelOutputMaxChars+1)
			wantSources := 2
			switch name {
			case "site icon":
				first["site_icon"] = "https://example.com/icon?" + longText
			case "source URL":
				first["url"] = firstURL + "?" + longText
				wantSources = 1
			case "unknown nested metadata":
				first["upstream_metadata"] = map[string]any{"large": longText}
			case "invalid date":
				first["date"] = longText
			case "oversized fractional timestamp":
				first["date"] = "2026-10-20T00:00:00." + strings.Repeat("0", contracts.ToolModelOutputMaxChars+1) + "Z"
			}
			sources := []any{first, second}
			result := invokeDeepSeekSearchPreview(t, sources)
			preview := decodeDeepSeekSearchPreview(t, result)
			items := preview["results"].([]any)
			if len(items) != wantSources || preview["totalResults"] != float64(2) || preview["omittedResults"] != float64(2-wantSources) || preview["contentTruncatedForLLM"] != true {
				t.Fatalf("oversized first result hid later sources: %#v", preview)
			}
			if items[len(items)-1].(map[string]any)["url"] != secondURL {
				t.Fatalf("second source URL must remain whole: %#v", items)
			}
			for _, raw := range items {
				item := raw.(map[string]any)
				if item["title"] != "来源标题🧑" {
					t.Fatalf("metadata omission should preserve short evidence: %#v", item)
				}
				if _, exists := item["site_icon"]; exists {
					t.Fatal("site icon must not consume the model preview budget")
				}
				if _, exists := item["upstream_metadata"]; exists {
					t.Fatal("unknown upstream extensions must not enter the preview")
				}
			}
			if !reflect.DeepEqual(result.Structured["results"], sources) {
				t.Fatal("model preview must not mutate complete structured evidence")
			}
		})
	}
}

func TestDeepSeekWebSearchPreviewSharesUnicodeTextBudget(t *testing.T) {
	first := deepSeekSearchPreviewSource(t, "https://example.com/first")
	second := deepSeekSearchPreviewSource(t, "https://example.com/second")
	content := strings.Repeat("正文🧑<>&\"\n", 10000)
	first["content"] = content
	second["content"] = content
	sources := []any{first, second}
	result := invokeDeepSeekSearchPreview(t, sources)
	preview := decodeDeepSeekSearchPreview(t, result)
	items := preview["results"].([]any)
	if len(items) != len(sources) || preview["omittedResults"] != float64(0) || preview["contentTruncatedForLLM"] != true {
		t.Fatalf("both sources must share the model text budget: %#v", preview)
	}
	for index, raw := range items {
		item := raw.(map[string]any)
		if item["url"] != sources[index].(map[string]any)["url"] || item["date"] != "2026-10-20T00:00:00Z" {
			t.Fatal("source URLs and valid publication timestamps must remain whole")
		}
		text := item["content"].(string)
		if text == "" || len(text) >= len(content) || !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) {
			t.Fatal("each source needs a bounded, non-empty UTF-8 excerpt")
		}
	}
	if !reflect.DeepEqual(result.Structured["results"], sources) {
		t.Fatal("model text truncation must preserve complete structured evidence")
	}
}

func TestDeepSeekWebSearchPreviewDistinguishesOmittedSourcesFromNoMatches(t *testing.T) {
	longURL := "https://example.com/?" + strings.Repeat("a", contracts.ToolModelOutputMaxChars+1)
	source := deepSeekSearchPreviewSource(t, longURL)
	result := invokeDeepSeekSearchPreview(t, []any{source})
	preview := decodeDeepSeekSearchPreview(t, result)
	if len(preview["results"].([]any)) != 0 || preview["resultsOmittedForLLM"] != true || preview["totalResults"] != float64(1) || preview["omittedResults"] != float64(1) {
		t.Fatalf("all-omitted sources must differ from an empty search: %#v", preview)
	}
	if !strings.Contains(preview["message"].(string), "does not mean the search found no matches") {
		t.Fatalf("model needs an explicit explanation for omitted sources: %#v", preview)
	}
	if !reflect.DeepEqual(result.Structured["results"], []any{source}) {
		t.Fatal("the full source URL must remain available in structured evidence")
	}
}

func TestDeepSeekWebSearchPreviewPreservesNormalResults(t *testing.T) {
	for _, sources := range [][]any{{}, {deepSeekSearchPreviewSource(t, "https://example.com/article")}} {
		result := invokeDeepSeekSearchPreview(t, sources)
		preview := decodeDeepSeekSearchPreview(t, result)
		encoded, err := json.Marshal(result.Structured)
		if err != nil {
			t.Fatal(err)
		}
		if result.Output != string(encoded) {
			t.Fatal("normal search output must remain identical to structured evidence")
		}
		if _, exists := preview["resultsOmittedForLLM"]; exists {
			t.Fatal("normal results must not be marked as omitted")
		}
	}
}

func TestDeepSeekWebSearchRejectsTooManyUpstreamResults(t *testing.T) {
	source := deepSeekSearchPreviewSource(t, "https://example.com/article")
	for _, count := range []int{50, 51} {
		sources := make([]any, count)
		for index := range sources {
			sources[index] = source
		}
		result := invokeDeepSeekSearchPreview(t, sources)
		if count == 50 {
			decodeDeepSeekSearchPreview(t, result)
		} else if result.Error != "web_search_response_invalid" || result.Structured["ok"] != false {
			t.Fatalf("more than 50 upstream results must be rejected before compaction: error=%q", result.Error)
		}
	}
}
