package tools

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/toolinput"
)

func (t *RuntimeToolExecutor) invokeWebSearch(ctx context.Context, args map[string]any) (ToolExecutionResult, error) {
	cfg := t.cfg.WebSearch
	if !cfg.Enabled {
		return modelToolError("web_search_disabled", "web_search is disabled by configs/tools.yml.", nil), nil
	}
	fields := map[string]string{"query": "s!", "originalQuestion": "s?", "localization": "s", "needContent": "b"}
	toolinput.NormalizeBooleanFields(args, fields)
	// Validate count separately: the API accepts exactly 1..50, including
	// programmatic Go callers using int rather than JSON's float64.
	validated := make(map[string]any, len(args))
	for key, value := range args {
		if key != "count" {
			validated[key] = value
		}
	}
	if err := toolinput.Validate(validated, fields, ""); err != nil {
		return webSearchInputError(err), nil
	}
	query := args["query"].(string)
	if utf8.RuneCountInString(query) > 128 {
		return modelToolError("web_search_input_invalid", "query must contain 1 to 128 Unicode characters.", nil), nil
	}
	body := map[string]any{"query": query}
	if value, exists := args["count"]; exists {
		var count float64
		switch v := value.(type) {
		case int:
			count = float64(v)
		case float64:
			count = v
		case json.Number:
			count, _ = v.Float64()
		}
		if count < 1 || count > 50 || math.Trunc(count) != count || math.IsNaN(count) {
			return modelToolError("web_search_input_invalid", "count must be an integer between 1 and 50.", nil), nil
		}
		body["count"] = int(count)
	}
	if value, exists := args["originalQuestion"]; exists && value != nil {
		question := value.(string)
		if n := utf8.RuneCountInString(question); n < 1 || n > 128 {
			return modelToolError("web_search_input_invalid", "originalQuestion must contain 1 to 128 Unicode characters, or be omitted/null.", nil), nil
		}
		body["ori_question"] = question
	}
	if value, exists := args["localization"]; exists {
		body["localization"] = value
	}
	if value, exists := args["needContent"]; exists {
		body["need_content"] = value
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(maxInt(cfg.Timeout, 60))*time.Second)
	defer cancel()
	start := time.Now()
	data, failure := t.postDeepSeekWeb(requestCtx, "web_search", cfg.ProviderKey, "search", body, cfg.MaxResponseBytes)
	if failure != nil {
		return *failure, nil
	}
	var response struct {
		Results []any `json:"results"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.Results == nil {
		return modelToolError("web_search_response_invalid", "The search API did not return a valid results array.", nil), nil
	}
	if len(response.Results) > 50 {
		return modelToolError("web_search_response_invalid", "The search API returned more than 50 results.", nil), nil
	}
	for _, result := range response.Results {
		if !validDeepSeekSearchResult(result) {
			return modelToolError("web_search_response_invalid", "The search API returned an invalid result or source URL.", nil), nil
		}
	}
	result := structuredResult(map[string]any{
		"ok": true, "query": query, "results": response.Results,
		"provider": cfg.ProviderKey, "durationMs": time.Since(start).Milliseconds(),
	})
	result.Output = webSearchModelOutput(result)
	return result, nil
}

func validDeepSeekSearchResult(value any) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, name := range []string{"title", "url", "snippet", "summary", "content"} {
		if _, ok := item[name].(string); !ok {
			return false
		}
	}
	for _, name := range []string{"site_name", "site_icon", "date"} {
		value, present := item[name]
		if !present {
			return false
		}
		if value != nil {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	score, ok := item["rerank_score"].(float64)
	if !ok || score < 0 || score > 3 {
		return false
	}
	authority, ok := item["authority"].(float64)
	if !ok || authority < -1 || authority > 4 || math.Trunc(authority) != authority {
		return false
	}
	u, err := url.Parse(item["url"].(string))
	return err == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// Keep the full structured evidence for history/UI consumers, while bounding the
// actual tool message. Share the text budget among sources so a single long page
// cannot hide every other source. URLs are never truncated into invalid links.
func webSearchModelOutput(result ToolExecutionResult) string {
	if len(result.Output) <= ToolModelOutputMaxChars {
		return result.Output
	}
	full := result.Structured["results"].([]any)
	preview := make(map[string]any, len(result.Structured)+3)
	for key, value := range result.Structured {
		preview[key] = value
	}
	preview["contentTruncatedForLLM"] = true
	preview["totalResults"] = len(full)
	preview["recovery"] = "Use web_fetch on the source URLs for more detail, or search with fewer results."
	build := func(sources []any, textLimit int) string {
		items := make([]any, 0, len(sources))
		for _, raw := range sources {
			source := raw.(map[string]any)
			item := make(map[string]any, 9)
			// Icons and arbitrary upstream extensions are not model evidence.
			for _, key := range []string{"url", "title", "snippet", "summary", "content", "site_name", "date", "rerank_score", "authority"} {
				value := source[key]
				if key == "date" {
					// Keep timestamps whole; malformed/oversized metadata cannot
					// consume the source URL budget or become a partial date.
					if date, ok := value.(string); ok {
						if len(date) > 64 {
							value = nil
						} else if _, err := time.Parse(time.RFC3339, date); err != nil {
							value = nil
						}
					}
				} else if text, ok := value.(string); ok && key != "url" && len(text) > textLimit {
					value = strings.ToValidUTF8(text[:textLimit], "")
				}
				item[key] = value
			}
			items = append(items, item)
		}
		preview["results"] = items
		preview["omittedResults"] = len(full) - len(sources)
		encoded, _ := json.Marshal(preview)
		return string(encoded)
	}
	sources := make([]any, 0, len(full))
	for _, source := range full {
		candidate := append(sources, source)
		if len(build(candidate, 0)) <= ToolModelOutputMaxChars {
			sources = candidate
		}
	}
	if len(sources) == 0 && len(full) > 0 {
		preview["resultsOmittedForLLM"] = true
		preview["message"] = "Search returned matching sources, but none of their complete URLs fit in the model output limit. The sources were omitted; this does not mean the search found no matches."
		preview["recovery"] = "Search with a more specific query to obtain other source URLs."
		return build(sources, 0)
	}
	best := build(sources, 0)
	low, high := 0, ToolModelOutputMaxChars
	for low <= high {
		mid := low + (high-low)/2
		candidate := build(sources, mid)
		if len(candidate) <= ToolModelOutputMaxChars {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
}

func webSearchInputError(err error) ToolExecutionResult {
	details := map[string]any{}
	if inputErr, ok := err.(*toolinput.Error); ok {
		details = inputErr.Details()
	}
	return modelToolError("web_search_input_invalid", err.Error(), details)
}
