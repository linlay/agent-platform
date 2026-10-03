package models

import (
	"sort"
	"strings"
)

// ProviderSummary is an allowlisted discovery projection. Endpoints, headers,
// compatibility payloads and credentials must never be serialized here.
type ProviderSummary struct {
	Key                  string   `json:"key"`
	Protocols            []string `json:"protocols"`
	CredentialConfigured bool     `json:"credentialConfigured"`
	DefaultModel         string   `json:"defaultModel"`
	ModelKeys            []string `json:"modelKeys"`
	ModelCount           int      `json:"modelCount"`
}

// ProviderSummaries includes providers with no models and reads both registries
// under one lock. Configured credentials are not a remote health check.
func (r *ModelRegistry) ProviderSummaries() []ProviderSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProviderSummary, 0, len(r.providers))
	for key, p := range r.providers {
		v := ProviderSummary{Key: key, Protocols: []string{}, ModelKeys: []string{}, CredentialConfigured: strings.TrimSpace(p.APIKey) != "", DefaultModel: p.DefaultModel}
		for protocol := range p.Protocols {
			v.Protocols = append(v.Protocols, protocol)
		}
		for key, model := range r.models {
			if model.Provider == p.Key {
				v.ModelKeys = append(v.ModelKeys, key)
			}
		}
		sort.Strings(v.Protocols)
		sort.Strings(v.ModelKeys)
		v.ModelCount = len(v.ModelKeys)
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}
