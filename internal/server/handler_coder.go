package server

import (
	"net/http"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"

	"agent-platform/internal/models"
)

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	response := s.buildModelOptionsForAgent(strings.TrimSpace(r.URL.Query().Get("agentKey")))
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptionsForAgent(agentKey string) api.CoderModelOptionsResponse {
	// External model choices belong to the prepared Chat, never the native catalog.
	if s.deps.Registry != nil {
		if def, ok := s.deps.Registry.AgentDefinition(agentKey); ok && def.Engine == catalog.AgentEngineACP {
			return api.CoderModelOptionsResponse{Models: []api.ChatModelOption{}}
		}
	}
	modelOptions := s.listModelOptionsForAgent(agentKey)
	reasoningEfforts := models.ReasoningEfforts()
	reasoningOptions := make([]api.ReasoningEffortOption, 0, len(reasoningEfforts))
	for _, effort := range reasoningEfforts {
		reasoningOptions = append(reasoningOptions, api.ReasoningEffortOption{Key: effort, Label: effort})
	}
	var responseModels []api.ChatModelOption
	if modelOptions != nil {
		responseModels = make([]api.ChatModelOption, 0, len(modelOptions))
	}
	for _, model := range modelOptions {
		option := api.ChatModelOption{
			Key:          model.Key,
			Name:         model.Name,
			Icon:         model.Icon,
			Provider:     model.Provider,
			ServiceTiers: model.ServiceTiers,
		}
		responseModels = append(responseModels, option)
	}
	return api.CoderModelOptionsResponse{
		Models:           responseModels,
		ReasoningEfforts: reasoningOptions,
	}
}

func (s *Server) listModelOptionsForAgent(agentKey string) []api.CoderModelOption {
	options := []api.CoderModelOption{}
	if s.deps.Models == nil {
		return options
	}
	for _, model := range s.deps.Models.List() {
		if !s.shouldShowModelOption(model) {
			continue
		}
		reasoningEfforts := append([]string(nil), model.ReasoningEfforts...)
		if model.IsReasoner {
			reasoningEfforts = models.ActiveReasoningEfforts()
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
			ReasoningEfforts: reasoningEfforts,
			ServiceTiers:     append([]string(nil), model.ServiceTiers...),
		})
	}
	return options
}

func (s *Server) shouldShowModelOption(model models.ModelDefinition) bool {
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
	return provider.HasCredential()
}

func normalizeCoderReasoningEffort(value string) (string, bool) {
	return agentbuiltin.CoderNormalizeReasoningEffort(value)
}
