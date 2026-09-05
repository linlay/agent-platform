// Package modelclient owns provider HTTP execution, first-response timeout,
// response classification, and stream body lifecycle.
package modelclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/apperrors"
)

type Client struct {
	http *http.Client
}

type Stream struct {
	Body   io.ReadCloser
	Cancel context.CancelFunc
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{http: httpClient}
}

func (c *Client) OpenStream(request *http.Request, firstResponseTimeout time.Duration) (*Stream, error) {
	if request == nil {
		return nil, apperrors.New(apperrors.CodeProviderBadRequest, "provider request is required")
	}
	if firstResponseTimeout <= 0 {
		body, err := c.do(request)
		if err != nil {
			return nil, err
		}
		return &Stream{Body: body}, nil
	}
	ctx, cancel := context.WithCancel(request.Context())
	timedRequest := request.WithContext(ctx)
	type result struct {
		body io.ReadCloser
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		body, err := c.do(timedRequest)
		resultCh <- result{body: body, err: err}
	}()
	timer := time.NewTimer(firstResponseTimeout)
	defer timer.Stop()
	select {
	case response := <-resultCh:
		if response.err != nil {
			cancel()
			return nil, response.err
		}
		if response.body == nil {
			cancel()
			return nil, errors.New("provider returned no stream")
		}
		return &Stream{Body: response.body, Cancel: cancel}, nil
	case <-timer.C:
		cancel()
		return nil, StreamTimeoutError(firstResponseTimeout)
	}
}

func (c *Client) do(request *http.Request) (io.ReadCloser, error) {
	client := c.http
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, TransportError(err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return nil, ResponseError(response.StatusCode, nil)
		}
		return nil, ResponseError(response.StatusCode, body)
	}
	return response.Body, nil
}

func StreamTimeoutError(timeout time.Duration) error {
	seconds := int64(timeout / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return apperrors.New(
		apperrors.CodeProviderTimeout,
		fmt.Sprintf("model stream idle timeout after %d seconds", seconds),
		apperrors.WithDiagnostic("timeoutSeconds", seconds),
		apperrors.WithDiagnostic("reason", "model_stream_idle_timeout"),
	)
}

func TransportError(err error) error {
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		return err
	}
	code := apperrors.CodeProviderNetworkError
	status := http.StatusBadGateway
	lower := strings.ToLower(err.Error())
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") {
		code = apperrors.CodeProviderTimeout
		status = http.StatusGatewayTimeout
	}
	return apperrors.Wrap(code, err, apperrors.WithStatus(status))
}

func ResponseError(status int, body []byte) error {
	bodyText := strings.TrimSpace(string(body))
	code, upstreamCode := ClassifyResponseError(status, bodyText)
	message := fmt.Sprintf("model request failed with status %d", status)
	if bodyText != "" {
		message += ": " + bodyText
	}
	diagnostics := map[string]any{"upstreamStatus": status}
	if bodyText != "" {
		diagnostics["upstreamBody"] = bodyText
	}
	if upstreamCode != "" {
		diagnostics["upstreamCode"] = upstreamCode
	}
	exposedStatus := status
	if code == apperrors.CodeProviderAuthFailed {
		exposedStatus = http.StatusBadGateway
	}
	return apperrors.New(code, message, apperrors.WithStatus(exposedStatus), apperrors.WithDiagnostics(diagnostics))
}

func ClassifyResponseError(status int, body string) (apperrors.Code, string) {
	signals := providerErrorSignals(body)
	combined := strings.ToLower(strings.Join(append([]string{body}, signals...), " "))
	upstreamCode := firstProviderSignal(signals)
	switch {
	case containsAny(combined, "context_length_exceeded", "context length", "maximum context", "token limit", "too many tokens"):
		return apperrors.CodeProviderContextLengthExceeded, upstreamCode
	case containsAny(combined, "content_filter", "content filter", "safety", "moderation", "blocked content"):
		return apperrors.CodeProviderContentFilter, upstreamCode
	case status == http.StatusUnauthorized:
		return apperrors.CodeProviderAuthFailed, upstreamCode
	case status == http.StatusForbidden:
		if containsQuotaSignal(combined) {
			return apperrors.CodeProviderQuotaExhausted, upstreamCode
		}
		return apperrors.CodeProviderPermissionDenied, upstreamCode
	case status == http.StatusNotFound:
		return apperrors.CodeProviderModelNotFound, upstreamCode
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return apperrors.CodeProviderTimeout, upstreamCode
	case status == http.StatusTooManyRequests:
		if containsQuotaSignal(combined) {
			return apperrors.CodeProviderQuotaExhausted, upstreamCode
		}
		return apperrors.CodeProviderRateLimited, upstreamCode
	case status == http.StatusRequestEntityTooLarge:
		return apperrors.CodeProviderContextLengthExceeded, upstreamCode
	case status >= 500:
		return apperrors.CodeProviderUnavailable, upstreamCode
	case status >= 400:
		return apperrors.CodeProviderBadRequest, upstreamCode
	default:
		return apperrors.CodeProviderRequestFailed, upstreamCode
	}
}

func providerErrorSignals(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return nil
	}
	var out []string
	collectProviderErrorSignals(decoded, &out)
	return out
}

func collectProviderErrorSignals(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if lowerKey == "code" || lowerKey == "type" || lowerKey == "error" || lowerKey == "message" || lowerKey == "detail" {
				if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
					*out = append(*out, strings.TrimSpace(text))
				}
			}
			collectProviderErrorSignals(child, out)
		}
	case []any:
		for _, child := range typed {
			collectProviderErrorSignals(child, out)
		}
	}
}

func firstProviderSignal(signals []string) string {
	for _, signal := range signals {
		signal = strings.TrimSpace(signal)
		if signal != "" && !strings.Contains(signal, " ") {
			return signal
		}
	}
	if len(signals) > 0 {
		return strings.TrimSpace(signals[0])
	}
	return ""
}

func containsQuotaSignal(text string) bool {
	return containsAny(text, "quota", "insufficient_quota", "exhausted", "billing", "credit", "balance", "recharge", "api key quota")
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
