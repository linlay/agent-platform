package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func parseOptionalBoolQuery(r *http.Request, key string) (*bool, error) {
	values, present := r.URL.Query()[key]
	if !present {
		return nil, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
	value := values[0] == "true"
	return &value, nil
}

func parseOptionalBoolPayload(raw json.RawMessage, key string) (*bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}
