package kbx

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

func selectedCollections(all []kbasescenter.Collection, selected []string) ([]kbasescenter.Collection, error) {
	out := []kbasescenter.Collection{}
	if len(selected) == 0 {
		for _, c := range all {
			if c.DefaultQuery == nil || *c.DefaultQuery {
				out = append(out, c)
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, name := range selected {
		found := false
		for _, c := range all {
			if name == c.Name {
				found = true
				if !seen[name] {
					out = append(out, c)
					seen[name] = true
				}
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown collection: %s", name)
		}
	}
	return out, nil
}

// Optional retrieval models never enter maintenance configuration or fingerprints.
func (m *Manager) queryConfig(raw []byte, d kbasescenter.Definition, method string, o *knowledge.SearchOptions) ([]byte, error) {
	if method != "query" && method != "vsearch" {
		return raw, nil
	}
	if o != nil {
		if o.Rerank != nil && *o.Rerank && (d.Models == nil || d.Models.Reranker == nil) {
			return nil, unavailable("rerank requires library models.reranker")
		}
		if o.QueryExpansion != nil && *o.QueryExpansion && (d.Models == nil || d.Models.QueryExpansion == nil) {
			return nil, unavailable("queryExpansion requires library models.queryExpansion")
		}
	}
	if d.Models == nil {
		return raw, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	registry, ok := m.models.(interface {
		GetTyped(string, string) (models.ModelDefinition, models.ProviderDefinition, error)
	})
	if m.options.ConfigSource != nil && m.options.ConfigSource.Registry != nil {
		registry = m.options.ConfigSource.Registry
		ok = true
	}
	for _, role := range []struct {
		name, kind string
		selected   *kbasescenter.QueryModelConfig
		enabled    bool
	}{
		{"reranker", models.ModelTypeReranker, d.Models.Reranker, method == "query" && (o == nil || o.Rerank == nil || *o.Rerank)},
		{"query_expansion", models.ModelTypeChat, d.Models.QueryExpansion, o == nil || o.QueryExpansion == nil || *o.QueryExpansion},
	} {
		if role.selected == nil || !role.enabled {
			continue
		}
		if !ok {
			return nil, unavailable("KBX query model registry unavailable")
		}
		model, provider, err := registry.GetTyped(role.selected.ModelKey, role.kind)
		if err != nil {
			return nil, err
		}
		protocol := strings.ToUpper(strings.TrimSpace(model.Protocol))
		if protocol != "" && protocol != "OPENAI" {
			return nil, fmt.Errorf("KBX %s requires an OPENAI-compatible HTTP model", role.name)
		}
		endpoint := model.Reranker.EndpointPath
		transport := provider.Protocol("OPENAI")
		if role.kind == models.ModelTypeChat {
			endpoint = transport.EndpointPath
		}
		if endpoint == "" {
			return nil, fmt.Errorf("KBX %s model endpoint is required", role.name)
		}
		if len(model.Headers) > 0 || len(model.Compat) > 0 || (role.kind == models.ModelTypeChat && (len(transport.Headers) > 0 || len(transport.Compat) > 0)) {
			return nil, fmt.Errorf("KBX %s does not support model custom headers or compat settings", role.name)
		}
		if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
			endpoint = strings.TrimRight(provider.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
		}
		timeout := model.Timeout
		if timeout <= 0 {
			timeout = 30
		}
		value := map[string]any{"url": endpoint, "model": model.ModelID, "timeout_ms": timeout * 1000}
		if provider.APIKey != "" {
			value["api_key"] = provider.APIKey
		}
		cfg["models"].(map[string]any)[role.name] = value
	}
	return json.Marshal(cfg)
}
