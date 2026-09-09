package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/modelclient"
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
	client := e.modelClient
	if client == nil {
		client = modelclient.New(e.httpClient)
	}
	opened, err := client.OpenStream(req, firstResponseTimeout)
	if err != nil {
		return nil, err
	}
	return &providerTurnStream{
		body:   opened.Body,
		reader: bufio.NewReader(opened.Body),
		cancel: opened.Cancel,
	}, nil
}

func providerTransportError(err error) error {
	return modelclient.TransportError(err)
}
