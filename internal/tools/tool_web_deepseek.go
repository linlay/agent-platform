package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/httpclient"
)

const deepSeekWebMaxAttempts = 3

// Search/Fetch use the provider's credentials, independently of the chat model.
// Never follow API redirects: even same-origin redirects can change POST semantics.
func (t *RuntimeToolExecutor) postDeepSeekWeb(ctx context.Context, toolName, providerKey, operation string, body any, maxBytes int) ([]byte, *ToolExecutionResult) {
	fail := func(code, message string, details map[string]any) ([]byte, *ToolExecutionResult) {
		result := modelToolError(toolName+"_"+code, message, details)
		return nil, &result
	}
	if t.models == nil {
		return fail("provider_unavailable", "Provider registry is unavailable.", nil)
	}
	if strings.TrimSpace(providerKey) == "" {
		return fail("provider_config_invalid", "Configure provider-key in configs/tools.yml.", nil)
	}
	provider, err := t.models.GetProvider(providerKey)
	if err != nil {
		return fail("provider_not_found", "Configured search/fetch provider was not found.", map[string]any{"provider": providerKey})
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return fail("provider_config_invalid", "Configure the search/fetch provider API key in the provider registry.", nil)
	}
	endpoint, err := deepSeekWebEndpoint(provider.BaseURL, operation)
	if err != nil {
		return fail("provider_config_invalid", err.Error(), nil)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fail("request_invalid", "Could not encode the search/fetch request.", nil)
	}
	baseClient := t.httpClient
	if baseClient == nil {
		baseClient = httpclient.NewClient(0)
	}
	client := *baseClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	limit := int64(maxInt(maxBytes, 10*1024*1024))
	for attempt := 0; attempt < deepSeekWebMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			return fail("request_invalid", "Could not create the search/fetch request.", nil)
		}
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			// Transport errors may contain URLs or proxy credentials. Keep them out
			// of model output; the request context still controls cancellation.
			message := "Search/fetch request failed before receiving a response."
			if ctx.Err() != nil {
				message = "Search/fetch request was canceled or exceeded its timeout."
			}
			return fail("request_failed", message, map[string]any{"retryable": ctx.Err() != context.Canceled})
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return fail("redirect_rejected", "The search/fetch API redirected the request. Check the provider baseUrl.", map[string]any{"httpStatus": resp.StatusCode, "retryable": false})
		}
		if int64(len(data)) > limit {
			return fail("response_too_large", "Search/fetch response exceeds max-response-bytes.", map[string]any{"maxResponseBytes": limit, "retryable": false})
		}
		if readErr != nil {
			return fail("request_failed", "Could not read the search/fetch response.", map[string]any{"retryable": ctx.Err() != context.Canceled})
		}
		if resp.StatusCode == http.StatusOK {
			return data, nil
		}
		retryable := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode <= 599
		if retryable && attempt+1 < deepSeekWebMaxAttempts {
			delay := deepSeekWebRetryDelay(resp.Header.Get("Retry-After"), attempt, resp.StatusCode)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fail("request_failed", "Search/fetch request was canceled or exceeded its timeout.", map[string]any{"retryable": ctx.Err() != context.Canceled})
			case <-timer.C:
				continue
			}
		}
		// Do not echo upstream error bodies: a proxy can reflect request secrets.
		return fail("http_error", deepSeekWebStatusMessage(resp.StatusCode), map[string]any{"httpStatus": resp.StatusCode, "retryable": retryable})
	}
	panic("unreachable")
}

func deepSeekWebEndpoint(baseURL, operation string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return "", fmt.Errorf("Provider baseUrl must be an HTTP(S) URL without credentials, query, or fragment.")
	}
	basePath := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(basePath, "/v1") {
		basePath += "/v1"
	}
	u.Path = basePath + "/tools/" + operation
	u.RawPath = ""
	return u.String(), nil
}

func deepSeekWebRetryDelay(header string, attempt, status int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 && seconds <= 86400 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(header); err == nil {
		if delay := time.Until(date); delay > 0 && delay <= 24*time.Hour {
			return delay
		}
	}
	if status == http.StatusTooManyRequests {
		// Leave the current per-second rate-limit window before retrying.
		// The caller's context still bounds the total wait and request time.
		return time.Duration(attempt+1) * time.Second
	}
	return time.Duration(attempt+1) * 250 * time.Millisecond
}

func deepSeekWebStatusMessage(status int) string {
	switch status {
	case 400:
		return "The search/fetch API rejected the request parameters."
	case 401:
		return "The search/fetch API key is missing or invalid."
	case 402:
		return "The search/fetch provider account has insufficient balance."
	case 403:
		return "The search/fetch API rejected the request under its access policy."
	case 404:
		return "The search/fetch API could not retrieve usable content. Check the URL and provider endpoint."
	case 408, 504:
		return "The search/fetch API timed out."
	case 413:
		return "The search/fetch API rejected an oversized request."
	case 429:
		return "The search/fetch API rate limit was exceeded."
	default:
		return fmt.Sprintf("Search/fetch API returned HTTP %d.", status)
	}
}

func (t *RuntimeToolExecutor) fetchDeepSeekWebContent(ctx context.Context, rawURL string, profile config.WebFetchProfileConfig) (*webFetchContent, *ToolExecutionResult) {
	limit := maxInt(profile.MaxURLLength, 8192)
	if utf8.RuneCountInString(rawURL) > limit || utf8.RuneCountInString(rawURL) > 8192 {
		result := modelToolError("web_fetch_url_invalid", "URL exceeds the configured limit or the DeepSeek 8192-character limit.", nil)
		return nil, &result
	}
	u, err := url.Parse(rawURL)
	if err == nil {
		// Reuse the public-URL policy, after checking DeepSeek's character limit.
		err = validateParsedWebFetchURL(u, config.WebFetchProfileConfig{MaxURLLength: len(u.String())})
	}
	if err != nil {
		result := modelToolError("web_fetch_url_invalid", "Provide a public HTTP(S) URL without credentials or a private host.", nil)
		return nil, &result
	}
	data, failure := t.postDeepSeekWeb(ctx, "web_fetch", profile.ProviderKey, "fetch", map[string]any{"url": rawURL}, profile.MaxResponseBytes)
	if failure != nil {
		return nil, failure
	}
	var page struct {
		Title   *string `json:"title"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(data, &page); err != nil || page.Title == nil || page.Content == nil || strings.TrimSpace(*page.Content) == "" {
		result := modelToolError("web_fetch_response_invalid", "The search/fetch API did not return a valid page title and Markdown content.", nil)
		return nil, &result
	}
	return &webFetchContent{
		Title:       *page.Title,
		Content:     *page.Content,
		Bytes:       len([]byte(*page.Content)),
		Code:        http.StatusOK,
		CodeText:    http.StatusText(http.StatusOK),
		ContentType: "text/markdown",
		FinalURL:    rawURL,
	}, nil
}
