package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/models"
)

var coderReasoningEfforts = []api.ReasoningEffortOption{
	{Key: "NONE", Label: "NONE"},
	{Key: "LOW", Label: "LOW"},
	{Key: "MEDIUM", Label: "MEDIUM"},
	{Key: "HIGH", Label: "HIGH"},
}

func (s *Server) handleModelOptions(w http.ResponseWriter, r *http.Request) {
	response := s.buildModelOptions()
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) buildModelOptions() api.CoderModelOptionsResponse {
	modelOptions := s.listModelOptions()
	defaultModelKey := s.defaultModelOptionKey(modelOptions)
	return api.CoderModelOptionsResponse{
		Models:                 modelOptions,
		ReasoningEfforts:       append([]api.ReasoningEffortOption(nil), coderReasoningEfforts...),
		DefaultModelKey:        defaultModelKey,
		DefaultReasoningEffort: "MEDIUM",
	}
}

func (s *Server) listModelOptions() []api.CoderModelOption {
	options := []api.CoderModelOption{}
	if s.deps.Models == nil {
		return options
	}
	for _, model := range s.deps.Models.List() {
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
		})
	}
	return options
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
