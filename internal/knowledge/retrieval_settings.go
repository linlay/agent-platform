package knowledge

import (
	"encoding/json"
	"fmt"
)

// RetrievalSettings preserves omission, including explicit zero scores and false switches.
type RetrievalSettings struct {
	TopK                *int     `json:"topK,omitempty"`
	CandidateFloor      *int     `json:"candidateFloor,omitempty"`
	CandidateMultiplier *int     `json:"candidateMultiplier,omitempty"`
	CandidateMax        *int     `json:"candidateMax,omitempty"`
	MinScore            *float64 `json:"minScore,omitempty"`
	RecencyWeight       *float64 `json:"recencyWeight,omitempty"`
	RecencyHalfLifeDays *float64 `json:"recencyHalfLifeDays,omitempty"`
	Rerank              *bool    `json:"rerank,omitempty"`
	QueryExpansion      *bool    `json:"queryExpansion,omitempty"`
}

func (s RetrievalSettings) Apply(c RetrievalConfig) RetrievalConfig {
	for _, v := range []struct {
		src *int
		dst *int
	}{{s.TopK, &c.TopK}, {s.CandidateFloor, &c.CandidateFloor}, {s.CandidateMultiplier, &c.CandidateMultiplier}, {s.CandidateMax, &c.CandidateMax}} {
		if v.src != nil {
			*v.dst = *v.src
		}
	}
	if s.MinScore != nil {
		c.MinScore = s.MinScore
	}
	if s.RecencyWeight != nil {
		c.RecencyWeight = s.RecencyWeight
	}
	if s.RecencyHalfLifeDays != nil {
		c.RecencyHalfLifeDays = s.RecencyHalfLifeDays
	}
	if s.Rerank != nil {
		c.Rerank = s.Rerank
	}
	if s.QueryExpansion != nil {
		c.QueryExpansion = s.QueryExpansion
	}
	return c
}
func ParseRetrievalSettings(node map[string]any) (RetrievalSettings, error) {
	var s RetrievalSettings
	// Preserve existing integer-string compatibility in Agent YAML.
	values := map[string]any{}
	for key, value := range node {
		switch key {
		case "topK", "candidateFloor", "candidateMultiplier", "candidateMax":
			parsed, ok := parseConfigInt(value)
			if !ok {
				return s, fmt.Errorf("retrieval.%s must be an integer", key)
			}
			values[key] = parsed
		case "minScore", "recencyWeight", "recencyHalfLifeDays", "rerank", "queryExpansion":
			if value == nil {
				return s, fmt.Errorf("retrieval.%s must not be null", key)
			}
			values[key] = value
		default:
			return s, fmt.Errorf("retrieval.%s is not supported", key)
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("invalid retrieval settings: %w", err)
	}
	return s, nil
}
func ValidateRetrieval(c RetrievalConfig) error {
	if c.TopK < 1 || c.TopK > 50 {
		return fmt.Errorf("retrieval.topK must be between 1 and 50")
	}
	if c.CandidateFloor < c.TopK {
		return fmt.Errorf("retrieval.candidateFloor must be at least topK")
	}
	if c.CandidateMultiplier < 1 || c.CandidateMultiplier > 2000 {
		return fmt.Errorf("retrieval.candidateMultiplier must be between 1 and 2000")
	}
	if c.CandidateMax < c.CandidateFloor || c.CandidateMax > 2000 {
		return fmt.Errorf("retrieval.candidateMax must be between candidateFloor and 2000")
	}
	_, err := NormalizeSearchOptions(SearchOptions{MinScore: c.MinScore, RecencyWeight: c.RecencyWeight, RecencyHalfLifeDays: c.RecencyHalfLifeDays})
	return err
}
func MergeRetrieval(library *RetrievalSettings, agent Config) (RetrievalConfig, error) {
	c := DefaultConfig().Retrieval
	if library != nil {
		c = library.Apply(c)
	}
	if agent.RetrievalOverrides != nil {
		c = agent.RetrievalOverrides.Apply(c)
	} else {
		// Programmatic callers may supply an effective configuration without YAML.
		baseline := DefaultConfig().Retrieval
		a := agent.Retrieval
		if a.TopK != 0 && a.TopK != baseline.TopK {
			c.TopK = a.TopK
		}
		if a.CandidateFloor != 0 && a.CandidateFloor != baseline.CandidateFloor {
			c.CandidateFloor = a.CandidateFloor
		}
		if a.CandidateMultiplier != 0 && a.CandidateMultiplier != baseline.CandidateMultiplier {
			c.CandidateMultiplier = a.CandidateMultiplier
		}
		if a.CandidateMax != 0 && a.CandidateMax != baseline.CandidateMax {
			c.CandidateMax = a.CandidateMax
		}
		c = (RetrievalSettings{MinScore: a.MinScore, RecencyWeight: a.RecencyWeight, RecencyHalfLifeDays: a.RecencyHalfLifeDays, Rerank: a.Rerank, QueryExpansion: a.QueryExpansion}).Apply(c)
	}
	return c, ValidateRetrieval(c)
}
func (c RetrievalConfig) ApplySearchDefaults(o SearchOptions) SearchOptions {
	if o.Limit == 0 {
		o.Limit = c.TopK
	}
	if o.Method != "gsearch" {
		if o.MinScore == nil {
			o.MinScore = c.MinScore
		}
		if o.RecencyWeight == nil {
			o.RecencyWeight = c.RecencyWeight
		}
		if o.RecencyHalfLifeDays == nil {
			o.RecencyHalfLifeDays = c.RecencyHalfLifeDays
		}
	}
	if o.Method == "query" {
		if o.Rerank == nil {
			o.Rerank = c.Rerank
		}
		if o.QueryExpansion == nil {
			o.QueryExpansion = c.QueryExpansion
		}
	} else if o.Method == "vsearch" && o.QueryExpansion == nil {
		o.QueryExpansion = c.QueryExpansion
	}
	return o
}

func ValidateRetrievalSettings(s RetrievalSettings) error {
	for _, v := range []struct {
		name     string
		value    *int
		min, max int
	}{{"topK", s.TopK, 1, 50}, {"candidateFloor", s.CandidateFloor, 1, 2000}, {"candidateMultiplier", s.CandidateMultiplier, 1, 2000}, {"candidateMax", s.CandidateMax, 1, 2000}} {
		if v.value != nil && (*v.value < v.min || *v.value > v.max) {
			return fmt.Errorf("retrieval.%s must be between %d and %d", v.name, v.min, v.max)
		}
	}
	if s.TopK != nil && s.CandidateFloor != nil && *s.TopK > *s.CandidateFloor {
		return fmt.Errorf("retrieval.candidateFloor must be at least topK")
	}
	if s.CandidateFloor != nil && s.CandidateMax != nil && *s.CandidateFloor > *s.CandidateMax {
		return fmt.Errorf("retrieval.candidateMax must be at least candidateFloor")
	}
	_, err := NormalizeSearchOptions(SearchOptions{MinScore: s.MinScore, RecencyWeight: s.RecencyWeight, RecencyHalfLifeDays: s.RecencyHalfLifeDays})
	return err
}
