package kbx

import (
	"encoding/json"
	"fmt"
)

// Center score is query-to-chunk vector similarity, never reciprocal rank or
// BM25/graph relevance. Keep the original ranking score and row order separately.
func withSimilarityScores(raw json.RawMessage, method string) (json.RawMessage, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(data["results"], &rows); err != nil {
		return nil, fmt.Errorf("invalid KBX search results: %w", err)
	}
	for _, row := range rows {
		if rankScore, ok := row["score"]; ok {
			row["rankingScore"] = rankScore
		}
		var similarity *float64
		switch method {
		case "vsearch":
			if value, ok := row["score"]; ok {
				if err := json.Unmarshal(value, &similarity); err != nil {
					return nil, fmt.Errorf("invalid KBX vector score")
				}
			}
		case "query":
			var explain struct {
				RRF struct {
					Contributions []struct {
						Source       string
						QueryType    string
						BackendScore *float64
					}
				}
			}
			if value, ok := row["explain"]; ok {
				if err := json.Unmarshal(value, &explain); err != nil {
					return nil, fmt.Errorf("invalid KBX score explanation")
				}
				for _, c := range explain.RRF.Contributions {
					// Expanded-query scores describe another query and cannot be substituted.
					if c.Source == "vec" && c.QueryType == "original" && c.BackendScore != nil {
						similarity = c.BackendScore
						break
					}
				}
			}
		}
		row["score"] = json.RawMessage("null")
		row["scoreType"] = json.RawMessage(`"unavailable"`)
		if similarity != nil {
			row["score"], _ = json.Marshal(*similarity)
			row["scoreType"] = json.RawMessage(`"vector_similarity"`)
		}
	}
	var err error
	if rows == nil {
		rows = []map[string]json.RawMessage{}
	}
	data["results"], err = json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	return json.Marshal(data)
}
