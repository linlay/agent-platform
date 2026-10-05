package memoryworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

// ConfigSync supplies a complete connection snapshot; model work belongs to memx.
type ConfigSync interface{ Sync(context.Context) error }
type ConfigSyncFunc func(context.Context) error

func (f ConfigSyncFunc) Sync(ctx context.Context) error { return f(ctx) }

type ModelConfigSync struct {
	Models         *models.ModelRegistry
	ModelKey       string
	TimeoutSeconds int
	Client         Client
	mu             sync.Mutex
	last           string
}

func (s *ModelConfigSync) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Models == nil {
		return fmt.Errorf("memory model registry unavailable")
	}
	model, provider, err := s.Models.Get(s.ModelKey)
	if err != nil {
		return fmt.Errorf("memory model configuration unavailable")
	}
	snapshot, err := connectionSnapshot(model, provider, s.TimeoutSeconds)
	if err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("invalid memory model configuration")
	}
	hash := digest(string(data))
	if hash == s.last {
		return nil
	}
	if err = s.Client.SetConfig(ctx, data); err != nil {
		return err
	}
	s.last = hash
	return nil
}
func connectionSnapshot(m models.ModelDefinition, p models.ProviderDefinition, timeout int) (map[string]any, error) {
	protocol := strings.ToUpper(strings.TrimSpace(m.Protocol))
	if protocol == "" {
		protocol = "OPENAI"
	}
	wire := ""
	path := ""
	auth := "bearer"
	switch protocol {
	case "OPENAI":
		wire = "openai_chat"
		path = "/chat/completions"
	case "OPENAI_RESPONSES":
		wire = "openai_responses"
		path = "/responses"
	case "ANTHROPIC":
		wire = "anthropic_messages"
		path = "/messages"
		auth = "api_key"
	default:
		return nil, fmt.Errorf("memory model protocol unsupported")
	}
	def := p.Protocol(protocol)
	endpoint := def.EndpointPath
	base := strings.TrimRight(p.BaseURL, "/")
	if endpoint == "" {
		endpoint = path
		if !strings.HasSuffix(base, "/v1") {
			endpoint = "/v1" + path
		}
	}
	if m.ModelID == "" || p.APIKey == "" || base == "" {
		return nil, fmt.Errorf("memory model connection incomplete")
	}
	if m.Timeout > 0 && m.Timeout < timeout {
		timeout = m.Timeout
	}
	headers := map[string]string{}
	for k, v := range def.Headers {
		headers[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range m.Headers {
		headers[http.CanonicalHeaderKey(k)] = v
	}
	// Authentication headers become the explicit auth field, never extra headers.
	for k, v := range headers {
		switch strings.ToLower(k) {
		case "authorization":
			if !strings.HasPrefix(v, "Bearer ") {
				return nil, fmt.Errorf("unsupported memory model authorization")
			}
			auth = "bearer"
			p.APIKey = strings.TrimPrefix(v, "Bearer ")
			delete(headers, k)
		case "x-api-key":
			auth = "api_key"
			p.APIKey = v
			delete(headers, k)
		}
	}
	role := map[string]any{
		"protocol": wire, "url": base + "/" + strings.TrimLeft(endpoint, "/"), "model": m.ModelID,
		"auth": map[string]any{"type": auth, "apiKey": p.APIKey}, "timeoutSeconds": timeout,
		"headers": headers, "parameters": map[string]any{"maxOutputTokens": 4096},
	}
	extras := mergeMemoryRequestExtras(memoryRequestAlways(def.Compat), memoryRequestAlways(m.Compat))
	if len(extras) > 0 {
		role["request"] = map[string]any{"extraBody": extras}
	}
	return map[string]any{"schemaVersion": 1, "models": map[string]any{"extraction": role}}, nil
}

// The host resolves registry policy. MEMX receives only the final wire fields;
// an extraction call does not opt into interactive reasoning overrides.
func memoryRequestAlways(compat map[string]any) map[string]any {
	request, _ := compat["request"].(map[string]any)
	always, _ := request["always"].(map[string]any)
	return always
}
func mergeMemoryRequestExtras(base, override map[string]any) map[string]any {
	result := contracts.CloneAnyMap(base)
	if result == nil {
		result = map[string]any{}
	}
	for key, value := range contracts.CloneAnyMap(override) {
		left, lok := result[key].(map[string]any)
		right, rok := value.(map[string]any)
		if lok && rok {
			result[key] = mergeMemoryRequestExtras(left, right)
		} else {
			result[key] = value
		}
	}
	return result
}
