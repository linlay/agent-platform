package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	agentcoder "agent-platform/internal/agent/coder"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
)

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	response := s.buildModelOptionsForAgent(strings.TrimSpace(r.URL.Query().Get("agentKey")))
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptionsForAgent(agentKey string) api.CoderModelOptionsResponse {
	modelOptions := s.listModelOptionsForAgent(agentKey)
	defaultModelKey := s.defaultModelOptionKeyForAgent(modelOptions, agentKey)
	reasoningEfforts := s.reasoningEffortOptionsForAgent(agentKey, modelOptions)
	serviceTiers := s.serviceTierOptionsForAgent(agentKey, modelOptions)
	defaultServiceTier := s.defaultServiceTierForAgent(agentKey, serviceTiers)
	if defaultServiceTier == "" {
		defaultServiceTier = "STANDARD"
	}
	return api.CoderModelOptionsResponse{
		Models:                 modelOptions,
		ReasoningEfforts:       reasoningEfforts,
		ServiceTiers:           serviceTiers,
		DefaultModelKey:        defaultModelKey,
		DefaultReasoningEffort: "MEDIUM",
		DefaultServiceTier:     defaultServiceTier,
	}
}

func coderModelConfigFromOptions(options api.CoderModelOptionsResponse) map[string]any {
	return agentcoder.ModelConfigFromOptions(options)
}

func modelConfigString(modelConfig map[string]any, key string) string {
	if len(modelConfig) == 0 {
		return ""
	}
	return strings.TrimSpace(stringValue(modelConfig[key]))
}

func coderModelConfigReasoningEffort(modelConfig map[string]any) string {
	return agentcoder.ModelConfigReasoningEffort(modelConfig)
}

func (s *Server) listModelOptionsForAgent(agentKey string) []api.CoderModelOption {
	if options, _, ok := s.listACPCoderModelOptions(agentKey); ok {
		return options
	}
	options := []api.CoderModelOption{}
	if s.deps.Models == nil {
		return options
	}
	filterMode := s.modelOptionsFilterMode(agentKey)
	for _, model := range s.deps.Models.List() {
		switch filterMode {
		case "acp-only":
			if !models.IsACPPassthroughModel(model) {
				continue
			}
		case "native-only":
			if models.IsACPPassthroughModel(model) {
				continue
			}
		}
		if !s.shouldShowModelOption(model) {
			continue
		}
		options = append(options, api.CoderModelOption{
			Key:              model.Key,
			Name:             model.Name,
			Icon:             model.Icon,
			Provider:         model.Provider,
			ModelID:          model.ModelID,
			Protocol:         model.Protocol,
			IsReasoner:       model.IsReasoner,
			IsVision:         model.IsVision,
			ContextWindow:    model.ContextWindow,
			Timeout:          model.Timeout,
			ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...),
			ServiceTiers:     append([]string(nil), model.ServiceTiers...),
		})
	}
	return options
}

func (s *Server) listACPCoderModelOptions(agentKey string) ([]api.CoderModelOption, error, bool) {
	bridge, ok := s.acpBridgeConfigForAgent(agentKey)
	if !ok {
		return nil, nil, false
	}
	options, err := fetchACPCoderModelOptions(bridge)
	if err != nil {
		log.Printf("[coder-model-options] fetch acp models for agent %s failed: %v", strings.TrimSpace(agentKey), err)
		return nil, err, true
	}
	return options, nil, true
}

func (s *Server) modelOptionsFilterMode(agentKey string) string {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return "native-only"
	}
	if s.deps.Registry == nil {
		return ""
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return ""
	}
	return agentcoder.ModelOptionsFilterMode(agentKey, def.Mode, def.ACPBridgeID)
}

func (s *Server) shouldShowModelOption(model models.ModelDefinition) bool {
	if models.IsACPPassthroughModel(model) {
		return true
	}
	if !models.IsChatModel(model) {
		return false
	}
	providerKey := strings.TrimSpace(model.Provider)
	if providerKey == "" || s.deps.Models == nil {
		return false
	}
	provider, err := s.deps.Models.GetProvider(providerKey)
	if err != nil {
		return false
	}
	return strings.TrimSpace(provider.APIKey) != ""
}

func (s *Server) defaultModelOptionKeyForAgent(options []api.CoderModelOption, agentKey string) string {
	if len(options) == 0 {
		return ""
	}
	preferredKey := ""
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(strings.TrimSpace(agentKey)); ok {
			preferredKey = strings.TrimSpace(def.ModelKey)
		}
	}
	defaultKey := ""
	if s.deps.Models != nil {
		if model, _, err := s.deps.Models.Default(); err == nil {
			defaultKey = strings.TrimSpace(model.Key)
		}
	}
	return agentcoder.DefaultModelOptionKey(options, preferredKey, defaultKey)
}

func (s *Server) defaultServiceTierForAgent(agentKey string, options []api.ServiceTierOption) string {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return ""
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return ""
	}
	return agentcoder.DefaultServiceTier(catalog.AgentUsesACPCoderBackend(def), def.ServiceTier, options)
}

func (s *Server) serviceTierOptionsForAgent(agentKey string, modelOptions []api.CoderModelOption) []api.ServiceTierOption {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return agentcoder.ServiceTierOptions(false, modelOptions)
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return agentcoder.ServiceTierOptions(false, modelOptions)
	}
	return agentcoder.ServiceTierOptions(catalog.AgentUsesACPCoderBackend(def), modelOptions)
}

func (s *Server) reasoningEffortOptionsForAgent(agentKey string, modelOptions []api.CoderModelOption) []api.ReasoningEffortOption {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return agentcoder.ReasoningEffortOptions(false, modelOptions)
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return agentcoder.ReasoningEffortOptions(false, modelOptions)
	}
	return agentcoder.ReasoningEffortOptions(catalog.AgentUsesACPCoderBackend(def), modelOptions)
}

func serviceTierInOptions(serviceTier string, options []api.ServiceTierOption) bool {
	return agentcoder.ServiceTierInOptions(serviceTier, options)
}

func serviceTierLabel(serviceTier string) string {
	return agentcoder.ServiceTierLabel(serviceTier)
}

func normalizeCoderReasoningEffort(value string) (string, bool) {
	return agentcoder.NormalizeReasoningEffort(value)
}

type acpModelCatalogResponse struct {
	Code int `json:"code"`
	Data struct {
		Models []struct {
			Key              string   `json:"key"`
			Name             string   `json:"name"`
			Icon             string   `json:"icon"`
			ModelID          string   `json:"modelId"`
			ContextWindow    int      `json:"contextWindow"`
			IsReasoner       bool     `json:"isReasoner"`
			ReasoningEfforts []string `json:"reasoningEfforts"`
			ServiceTiers     []string `json:"serviceTiers"`
		} `json:"models"`
	} `json:"data"`
}

func (s *Server) acpBridgeConfigForAgent(agentKey string) (config.CoderACPBridgeConfig, bool) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return config.CoderACPBridgeConfig{}, false
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || !catalog.AgentUsesACPCoderBackend(def) {
		return config.CoderACPBridgeConfig{}, false
	}
	bridgeID := strings.TrimSpace(def.ACPBridgeID)
	if bridgeID == "" {
		return config.CoderACPBridgeConfig{}, false
	}
	bridge, ok := s.deps.Config.CoderSettings.ACPBridges[bridgeID]
	if !ok || strings.TrimSpace(bridge.BaseURL) == "" {
		return config.CoderACPBridgeConfig{}, false
	}
	return bridge, true
}

func fetchACPCoderModelOptions(bridge config.CoderACPBridgeConfig) ([]api.CoderModelOption, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(bridge.BaseURL), "/")
	if baseURL == "" {
		return nil, nil
	}
	timeout := time.Duration(bridge.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/models", nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(bridge.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{status: resp.StatusCode, message: "proxy model discovery returned " + resp.Status}
	}
	var decoded acpModelCatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	options := make([]api.CoderModelOption, 0, len(decoded.Data.Models))
	for _, model := range decoded.Data.Models {
		key := strings.TrimSpace(model.Key)
		if key == "" {
			continue
		}
		options = append(options, api.CoderModelOption{
			Key:              key,
			Name:             strings.TrimSpace(model.Name),
			Icon:             strings.TrimSpace(model.Icon),
			ModelID:          strings.TrimSpace(model.ModelID),
			Protocol:         models.ProtocolACPPassthrough,
			IsReasoner:       model.IsReasoner,
			ContextWindow:    model.ContextWindow,
			ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...),
			ServiceTiers:     append([]string(nil), model.ServiceTiers...),
		})
	}
	return options, nil
}
