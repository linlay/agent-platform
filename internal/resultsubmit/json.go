package resultsubmit

import (
	"bytes"
	"encoding/json"
)

// jsonValue returns the JSON form of a decoded tool argument, so the validator
// sees exactly what a JSON consumer of the output receives.
func jsonValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var out any
	if err := decoder.Decode(&out); err != nil {
		return value
	}
	return out
}

func deepCloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = deepCloneValue(value)
	}
	return out
}

func deepCloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = deepCloneValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
