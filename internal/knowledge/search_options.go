package knowledge

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"agent-platform/internal/toolinput"
)

var searchArgumentNames = []string{
	"collections", "rerank", "queryExpansion", "query", "method", "limit", "offset", "pathPrefix", "pathGlob", "type", "filter",
	"exclude", "intent", "minScore", "candidateLimit", "recencyWeight", "recencyHalfLifeDays",
	"noGraph", "entities", "relations", "direction", "maxHops",
}

func searchOptionsFromArgs(args map[string]any) (SearchOptions, error) {
	// Copy before normalization: the invocation also feeds the audit record.
	values := make(map[string]any, len(args))
	for k, v := range args {
		if !slices.Contains(searchArgumentNames, k) {
			return SearchOptions{}, toolinput.Unknown("", searchArgumentNames)
		}
		if v == nil {
			return SearchOptions{}, toolinput.New(k, "non-null value of the declared type", v, true, "Omit optional parameters instead of passing null.")
		}
		values[k] = v
	}
	for _, key := range []string{"noGraph", "rerank", "queryExpansion"} {
		if v, present := values[key]; present {
			b, ok := toolinput.ParseBool(v)
			if !ok {
				return SearchOptions{}, fmt.Errorf("%s must be a boolean", key)
			}
			values[key] = b
		}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return SearchOptions{}, fmt.Errorf("invalid search parameters")
	}
	var options SearchOptions
	if err := json.Unmarshal(b, &options); err != nil {
		return SearchOptions{}, fmt.Errorf("invalid search parameter type: %w", err)
	}
	for _, field := range []struct {
		name  string
		value int
	}{{"candidateLimit", options.CandidateLimit}, {"maxHops", options.MaxHops}} {
		if _, present := args[field.name]; present && field.value <= 0 {
			return SearchOptions{}, fmt.Errorf("%s must be positive when specified", field.name)
		}
	}
	return NormalizeSearchOptions(options)
}

// NormalizeSearchOptions is shared by tool and service callers. CLI-only flags
// must never silently disappear when a caller chooses another retrieval method.
func NormalizeSearchOptions(o SearchOptions) (SearchOptions, error) {
	if o.Collections != nil && len(o.Collections) == 0 {
		return o, fmt.Errorf("collections must not be empty when specified")
	}
	o.Method = strings.TrimSpace(o.Method)
	if o.Method == "" {
		o.Method = "query"
	}
	switch o.Method {
	case "query", "search", "vsearch", "gsearch":
	default:
		return o, fmt.Errorf("method must be query, search, vsearch, or gsearch")
	}
	if o.Offset != 0 {
		return o, fmt.Errorf("KBX chunk search does not support offset")
	}
	if o.Limit < 0 || o.Limit > 50 {
		return o, fmt.Errorf("limit must be between 1 and 50 when specified (0 uses the Agent default)")
	}
	if o.CandidateLimit < 0 || o.CandidateLimit > 2000 {
		return o, fmt.Errorf("candidateLimit must be between 1 and 2000 when specified")
	}
	for _, field := range []struct {
		name  string
		value *float64
	}{{"minScore", o.MinScore}, {"recencyWeight", o.RecencyWeight}, {"recencyHalfLifeDays", o.RecencyHalfLifeDays}} {
		if field.value != nil && (math.IsNaN(*field.value) || math.IsInf(*field.value, 0)) {
			return o, fmt.Errorf("%s must be finite", field.name)
		}
	}
	if o.RecencyWeight != nil && (*o.RecencyWeight < 0 || *o.RecencyWeight > 1) {
		return o, fmt.Errorf("recencyWeight must be between 0 and 1")
	}
	if o.RecencyHalfLifeDays != nil && *o.RecencyHalfLifeDays <= 0 {
		return o, fmt.Errorf("recencyHalfLifeDays must be positive")
	}
	for _, field := range []struct {
		name   string
		values []string
	}{{"collections", o.Collections}, {"exclude", o.Exclude}, {"entities", o.Entities}, {"relations", o.Relations}} {
		for _, value := range field.values {
			if strings.TrimSpace(value) == "" {
				return o, fmt.Errorf("%s entries must not be blank", field.name)
			}
		}
	}
	o.PathPrefix = strings.TrimSpace(o.PathPrefix)
	o.PathGlob = strings.TrimSpace(o.PathGlob)
	o.Type = strings.TrimSpace(o.Type)
	o.Filter = strings.TrimSpace(o.Filter)
	o.Intent = strings.TrimSpace(o.Intent)
	o.Direction = strings.TrimSpace(o.Direction)
	if o.Method == "gsearch" {
		if len(o.Exclude) > 0 || o.Intent != "" || o.MinScore != nil || o.CandidateLimit != 0 || o.RecencyWeight != nil || o.RecencyHalfLifeDays != nil || o.NoGraph {
			return o, fmt.Errorf("gsearch does not support exclude, intent, minScore, candidateLimit, recencyWeight, recencyHalfLifeDays, or noGraph; use filter for graph document constraints")
		}
		switch o.Direction {
		case "", "auto", "out", "in", "both":
		default:
			return o, fmt.Errorf("direction must be auto, out, in, or both")
		}
		if o.MaxHops < 0 || o.MaxHops > 3 {
			return o, fmt.Errorf("maxHops must be between 1 and 3 when specified")
		}
	} else if len(o.Entities) > 0 || len(o.Relations) > 0 || o.Direction != "" || o.MaxHops != 0 {
		return o, fmt.Errorf("entities, relations, direction, and maxHops require method gsearch")
	}
	if o.Rerank != nil && o.Method != "query" {
		return o, fmt.Errorf("rerank requires method query")
	}
	if o.QueryExpansion != nil && o.Method != "query" && o.Method != "vsearch" {
		return o, fmt.Errorf("queryExpansion requires query or vsearch")
	}
	if o.NoGraph && o.Method != "query" {
		return o, fmt.Errorf("noGraph requires method query")
	}
	return o, nil
}
