package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/models"
)

var coderReasoningEfforts = []api.ReasoningEffortOption{
	{Key: "NONE", Label: "NONE"},
	{Key: "LOW", Label: "LOW"},
	{Key: "MEDIUM", Label: "MEDIUM"},
	{Key: "HIGH", Label: "HIGH"},
}

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	response := s.buildModelOptionsForAgent(strings.TrimSpace(r.URL.Query().Get("agentKey")))
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptions() api.CoderModelOptionsResponse {
	return s.buildModelOptionsForAgent("")
}

func (s *Server) buildModelOptionsForAgent(agentKey string) api.CoderModelOptionsResponse {
	modelOptions := s.listModelOptionsForAgent(agentKey)
	defaultModelKey := s.defaultModelOptionKeyForAgent(modelOptions, agentKey)
	serviceTiers := s.serviceTierOptionsForAgent(agentKey, modelOptions)
	defaultServiceTier := s.defaultServiceTierForAgent(agentKey, serviceTiers)
	if defaultServiceTier == "" {
		defaultServiceTier = "STANDARD"
	}
	return api.CoderModelOptionsResponse{
		Models:                 modelOptions,
		ReasoningEfforts:       append([]api.ReasoningEffortOption(nil), coderReasoningEfforts...),
		ServiceTiers:           serviceTiers,
		DefaultModelKey:        defaultModelKey,
		DefaultReasoningEffort: "MEDIUM",
		DefaultServiceTier:     defaultServiceTier,
	}
}

func (s *Server) listModelOptions() []api.CoderModelOption {
	return s.listModelOptionsForAgent("")
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
			Key:           model.Key,
			Name:          model.Name,
			Provider:      model.Provider,
			ModelID:       model.ModelID,
			Protocol:      model.Protocol,
			IsReasoner:    model.IsReasoner,
			IsVision:      model.IsVision,
			ContextWindow: model.ContextWindow,
			ServiceTiers:  append([]string(nil), model.ServiceTiers...),
		})
	}
	return options
}

func (s *Server) listACPCoderModelOptions(agentKey string) ([]api.CoderModelOption, error, bool) {
	proxy, ok := s.acpProxyConfigForAgent(agentKey)
	if !ok {
		return nil, nil, false
	}
	options, err := fetchACPCoderModelOptions(proxy)
	if err != nil {
		log.Printf("[coder-model-options] fetch acp models for agent %s failed: %v", strings.TrimSpace(agentKey), err)
		return nil, err, true
	}
	return options, nil, true
}

func (s *Server) modelOptionsFilterMode(agentKey string) string {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return ""
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return ""
	}
	if catalog.AgentUsesACPCoderBackend(def) {
		return "acp-only"
	}
	if strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeCoder) {
		return "native-only"
	}
	return ""
}

func (s *Server) shouldShowModelOption(model models.ModelDefinition) bool {
	if models.IsACPPassthroughModel(model) {
		return true
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

func (s *Server) defaultModelOptionKey(options []api.CoderModelOption) string {
	return s.defaultModelOptionKeyForAgent(options, "")
}

func (s *Server) defaultModelOptionKeyForAgent(options []api.CoderModelOption, agentKey string) string {
	if len(options) == 0 {
		return ""
	}
	visible := make(map[string]bool, len(options))
	normalFallback := ""
	acpFallback := ""
	for _, option := range options {
		key := strings.TrimSpace(option.Key)
		if key == "" {
			continue
		}
		visible[key] = true
		if models.IsACPPassthroughProtocol(option.Protocol) {
			if acpFallback == "" {
				acpFallback = key
			}
			continue
		}
		if normalFallback == "" {
			normalFallback = key
		}
	}
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(strings.TrimSpace(agentKey)); ok {
			key := strings.TrimSpace(def.ModelKey)
			if visible[key] {
				return key
			}
		}
	}
	if s.deps.Models != nil {
		if model, _, err := s.deps.Models.Default(); err == nil {
			key := strings.TrimSpace(model.Key)
			if visible[key] {
				return key
			}
		}
	}
	if normalFallback != "" {
		return normalFallback
	}
	return acpFallback
}

func (s *Server) defaultServiceTierForAgent(agentKey string, options []api.ServiceTierOption) string {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return ""
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || !catalog.AgentUsesACPCoderBackend(def) {
		return ""
	}
	serviceTier, ok := normalizeQueryModelServiceTier(def.ServiceTier)
	if !ok || serviceTier == "" {
		return ""
	}
	if !serviceTierInOptions(serviceTier, options) {
		return ""
	}
	return serviceTier
}

func (s *Server) serviceTierOptionsForAgent(agentKey string, models []api.CoderModelOption) []api.ServiceTierOption {
	options := []api.ServiceTierOption{{Key: "STANDARD", Label: "Standard"}}
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return options
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || !catalog.AgentUsesACPCoderBackend(def) {
		return options
	}
	seen := map[string]struct{}{"STANDARD": {}}
	extra := make([]string, 0, 4)
	for _, model := range models {
		for _, rawTier := range model.ServiceTiers {
			tier, ok := normalizeQueryModelServiceTier(rawTier)
			if !ok || tier == "" {
				continue
			}
			if _, exists := seen[tier]; exists {
				continue
			}
			seen[tier] = struct{}{}
			extra = append(extra, tier)
		}
	}
	sort.Strings(extra)
	for _, tier := range extra {
		options = append(options, api.ServiceTierOption{
			Key:   tier,
			Label: serviceTierLabel(tier),
		})
	}
	return options
}

func serviceTierInOptions(serviceTier string, options []api.ServiceTierOption) bool {
	serviceTier = strings.TrimSpace(serviceTier)
	if serviceTier == "" {
		return true
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Key), serviceTier) {
			return true
		}
	}
	return false
}

func serviceTierLabel(serviceTier string) string {
	switch strings.ToUpper(strings.TrimSpace(serviceTier)) {
	case "STANDARD":
		return "Standard"
	case "FAST":
		return "Fast"
	case "FLEX":
		return "Flex"
	default:
		return strings.TrimSpace(serviceTier)
	}
}

func normalizeCoderReasoningEffort(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", true
	case "NONE":
		return "NONE", true
	case "LOW":
		return "LOW", true
	case "MEDIUM":
		return "MEDIUM", true
	case "HIGH":
		return "HIGH", true
	default:
		return "", false
	}
}

type acpModelCatalogResponse struct {
	Code int `json:"code"`
	Data struct {
		Models []struct {
			Key           string   `json:"key"`
			Name          string   `json:"name"`
			ModelID       string   `json:"modelId"`
			ContextWindow int      `json:"contextWindow"`
			IsReasoner    bool     `json:"isReasoner"`
			ServiceTiers  []string `json:"serviceTiers"`
		} `json:"models"`
	} `json:"data"`
}

func (s *Server) acpProxyConfigForAgent(agentKey string) (config.CoderACPProxyConfig, bool) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || s.deps.Registry == nil {
		return config.CoderACPProxyConfig{}, false
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || !catalog.AgentUsesACPCoderBackend(def) {
		return config.CoderACPProxyConfig{}, false
	}
	proxyID := strings.TrimSpace(def.ACPProxyID)
	if proxyID == "" {
		return config.CoderACPProxyConfig{}, false
	}
	proxy, ok := s.deps.Config.CoderSettings.ACPProxies[proxyID]
	if !ok || strings.TrimSpace(proxy.BaseURL) == "" {
		return config.CoderACPProxyConfig{}, false
	}
	return proxy, true
}

func fetchACPCoderModelOptions(proxy config.CoderACPProxyConfig) ([]api.CoderModelOption, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(proxy.BaseURL), "/")
	if baseURL == "" {
		return nil, nil
	}
	timeout := time.Duration(proxy.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/models", nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(proxy.AuthToken); token != "" {
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
			Key:           key,
			Name:          strings.TrimSpace(model.Name),
			ModelID:       strings.TrimSpace(model.ModelID),
			Protocol:      models.ProtocolACPPassthrough,
			IsReasoner:    model.IsReasoner,
			ContextWindow: model.ContextWindow,
			ServiceTiers:  append([]string(nil), model.ServiceTiers...),
		})
	}
	return options, nil
}
