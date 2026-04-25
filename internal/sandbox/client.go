package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

type ContainerHubClient struct {
	baseURL    string
	authToken  string
	timeout    time.Duration
	httpClient *http.Client
}

type EnvironmentAgentPromptResult struct {
	EnvironmentName string
	HasPrompt       bool
	Prompt          string
	UpdatedAt       string
	Error           string
	OK              bool
}

type RuntimeInfo struct {
	Engine string
	OK     bool
}

func NewContainerHubClient(cfg config.ContainerHubConfig) *ContainerHubClient {
	return &ContainerHubClient{
		baseURL:   strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		authToken: strings.TrimSpace(cfg.AuthToken),
		timeout:   time.Duration(maxInt(cfg.RequestTimeoutMs, 1)) * time.Millisecond,
		httpClient: &http.Client{
			Timeout: time.Duration(maxInt(cfg.RequestTimeoutMs, 1)) * time.Millisecond,
		},
	}
}

func (c *ContainerHubClient) CreateSession(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return c.post(ctx, "/api/sessions/create", payload)
}

// ExecuteSessionRaw calls the container-hub execute API and returns the raw response.
// Container Hub returns plain text on success, JSON error envelope on failure.
// The response's Content-Type is authoritative (text/plain vs application/json);
// previously we detected "is this JSON?" by body parse, which false-positives when
// stdout happens to be valid JSON (e.g. dbx --format json output).
// Java: ContainerHubClient.executeSession with contentTypeAware=true
//
//	→ success (Content-Type: text/plain): body is raw stdout
//	→ failure (Content-Type: application/json): body is error envelope
func (c *ContainerHubClient) ExecuteSessionRaw(ctx context.Context, sessionID string, payload map[string]any) (string, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/sessions/"+strings.TrimSpace(sessionID)+"/execute", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, containerHubStatusError("/api/sessions/execute", resp.StatusCode, rawBody)
	}
	isJSON := strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))), "application/json")
	return string(rawBody), isJSON, nil
}

func (c *ContainerHubClient) StopSession(ctx context.Context, sessionID string) (map[string]any, error) {
	return c.post(ctx, "/api/sessions/"+strings.TrimSpace(sessionID)+"/stop", map[string]any{})
}

func (c *ContainerHubClient) GetRuntimeInfo() RuntimeInfo {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/runtime-info", nil)
	if err != nil {
		return RuntimeInfo{}
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RuntimeInfo{}
	}
	defer resp.Body.Close()

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return RuntimeInfo{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RuntimeInfo{}
	}
	return RuntimeInfo{
		Engine: strings.TrimSpace(firstStringValue(decoded, "engine")),
		OK:     true,
	}
}

func (c *ContainerHubClient) GetEnvironmentAgentPrompt(environmentID string) (EnvironmentAgentPromptResult, error) {
	normalized := strings.TrimSpace(environmentID)
	if normalized == "" {
		return EnvironmentAgentPromptResult{}, fmt.Errorf("environment id is required")
	}
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/api/environments/"+normalized+"/agent-prompt", nil)
	if err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return EnvironmentAgentPromptResult{}, err
	}
	result := EnvironmentAgentPromptResult{
		EnvironmentName: strings.TrimSpace(firstStringValue(decoded, "environmentName", "environment_name")),
		HasPrompt:       firstBoolValue(decoded, "hasPrompt", "has_prompt"),
		Prompt:          firstStringValue(decoded, "prompt"),
		UpdatedAt:       firstStringValue(decoded, "updatedAt", "updated_at"),
		OK:              resp.StatusCode >= 200 && resp.StatusCode < 300,
	}
	if !result.OK {
		result.Error = firstStringValue(decoded, "error")
		if strings.TrimSpace(result.Error) == "" {
			result.Error = fmt.Sprintf("status %d", resp.StatusCode)
		}
	}
	return result, nil
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(contracts.AnyStringNode(values[key])); value != "" {
			return value
		}
	}
	return ""
}

func firstBoolValue(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return contracts.AnyBoolNode(value)
		}
	}
	return false
}

func (c *ContainerHubClient) post(ctx context.Context, path string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &decoded); err != nil {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Printf("[container-hub] %s %d request=%s response=%s", path, resp.StatusCode, string(body), string(rawBody))
				return nil, containerHubStatusError(path, resp.StatusCode, rawBody)
			}
			return nil, err
		}
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := json.Marshal(decoded)
		log.Printf("[container-hub] %s %d request=%s response=%s", path, resp.StatusCode, string(body), string(detail))
		return nil, containerHubStatusError(path, resp.StatusCode, rawBody)
	}
	return decoded, nil
}

func containerHubStatusError(path string, statusCode int, rawBody []byte) error {
	detail := containerHubErrorDetail(rawBody)
	if detail == "" {
		return fmt.Errorf("%s returned status %d", path, statusCode)
	}
	return fmt.Errorf("%s returned status %d: %s", path, statusCode, detail)
}

func containerHubErrorDetail(rawBody []byte) string {
	trimmed := strings.TrimSpace(string(rawBody))
	if trimmed == "" {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(rawBody, &decoded); err == nil {
		if message := firstStringValue(decoded, "error", "message", "msg", "detail"); message != "" {
			return message
		}
		if detail, err := json.Marshal(decoded); err == nil {
			return string(detail)
		}
	}
	if len(trimmed) > 2048 {
		return trimmed[:2048] + "...(truncated)"
	}
	return trimmed
}
