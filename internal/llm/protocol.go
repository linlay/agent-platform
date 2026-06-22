package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/apperrors"
	. "agent-platform/internal/contracts"
	. "agent-platform/internal/models"
)

type providerProtocol interface {
	PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error)
	OpenStream(ctx context.Context, params protocolStreamParams, prepared preparedProviderRequest) (*providerTurnStream, error)
	ConsumeChunk(s *llmRunStream, eventName string, rawChunk string) (turnDone bool, err error)
}

type preparedProviderRequest struct {
	Endpoint        string
	RequestBody     map[string]any
	RequestBodyJSON []byte
	Headers         map[string]string
}

type protocolStreamParams struct {
	runID          string
	provider       ProviderDefinition
	model          ModelDefinition
	protocolConfig protocolRuntimeConfig
	stageSettings  StageSettings
	messages       []openAIMessage
	toolSpecs      []openAIToolSpec
	toolChoice     string
	modelTimeout   time.Duration
}

func resolveProtocol(engine *LLMAgentEngine, model ModelDefinition) providerProtocol {
	switch strings.ToUpper(strings.TrimSpace(model.Protocol)) {
	case "ANTHROPIC":
		return &anthropicProtocol{engine: engine}
	case "", "OPENAI":
		return &openAIProtocol{engine: engine}
	default:
		return nil
	}
}

func resolveProviderEndpoint(params protocolStreamParams) (string, error) {
	if params.provider.BaseURL == "" {
		return "", fmt.Errorf("provider %s has empty baseUrl", params.provider.Key)
	}
	if params.provider.APIKey == "" {
		return "", fmt.Errorf("provider %s has empty apiKey", params.provider.Key)
	}
	return strings.TrimRight(params.provider.BaseURL, "/") + params.protocolConfig.EndpointPath, nil
}

func normalizePreparedRequestBody(body []byte) (map[string]any, error) {
	var requestBody map[string]any
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return nil, err
	}
	return requestBody, nil
}

func (e *LLMAgentEngine) executeProviderRequest(req *http.Request, firstResponseTimeout time.Duration) (*providerTurnStream, error) {
	if firstResponseTimeout > 0 {
		return e.executeProviderRequestWithFirstResponseTimeout(req, firstResponseTimeout)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, providerTransportError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, providerResponseError(resp.StatusCode, nil)
		}
		return nil, providerResponseError(resp.StatusCode, data)
	}
	return &providerTurnStream{
		body:   resp.Body,
		reader: bufio.NewReader(resp.Body),
	}, nil
}

type providerRequestResult struct {
	turn *providerTurnStream
	err  error
}

func (e *LLMAgentEngine) executeProviderRequestWithFirstResponseTimeout(req *http.Request, timeout time.Duration) (*providerTurnStream, error) {
	ctx, cancel := context.WithCancel(req.Context())
	timedReq := req.WithContext(ctx)
	resultCh := make(chan providerRequestResult, 1)
	go func() {
		turn, err := e.executeProviderRequest(timedReq, 0)
		resultCh <- providerRequestResult{turn: turn, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		if result.turn != nil {
			result.turn.cancel = cancel
		}
		return result.turn, nil
	case <-timer.C:
		cancel()
		return nil, modelStreamIdleTimeoutError(timeout)
	}
}

func providerTransportError(err error) error {
	code := apperrors.CodeProviderNetworkError
	status := http.StatusBadGateway
	lower := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") {
		code = apperrors.CodeProviderTimeout
		status = http.StatusGatewayTimeout
	}
	return apperrors.Wrap(code, err, apperrors.WithStatus(status))
}

func providerResponseError(status int, body []byte) error {
	bodyText := strings.TrimSpace(string(body))
	code, upstreamCode := classifyProviderResponseError(status, bodyText)
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
	return apperrors.New(code, message, apperrors.WithStatus(status), apperrors.WithDiagnostics(diagnostics))
}

func classifyProviderResponseError(status int, body string) (apperrors.Code, string) {
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
	case status >= 400 && status < 500:
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
