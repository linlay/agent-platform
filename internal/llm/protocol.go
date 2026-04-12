package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	. "agent-platform-runner-go/internal/models"
)

type providerProtocol interface {
	OpenStream(ctx context.Context, params protocolStreamParams) (*providerTurnStream, error)
	ConsumeChunk(s *llmRunStream, eventName string, rawChunk string) (turnDone bool, err error)
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

func (e *LLMAgentEngine) executeProviderRequest(req *http.Request) (*providerTurnStream, error) {
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("model request failed with status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("model request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return &providerTurnStream{
		body:   resp.Body,
		reader: bufio.NewReader(resp.Body),
	}, nil
}
