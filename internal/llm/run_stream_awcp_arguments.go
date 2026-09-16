package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const awcpMaxSafeJSONNumber = 9007199254740991

func awcpMethodInValidJSON(raw string) bool {
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return false
	}
	return envelope.Method == desktopAwcpSnapshotMethod || envelope.Method == desktopAwcpInvokeMethod
}

// AWCP arguments remain JSON values until Desktop receives them. The ordinary
// CDP path retains its existing argument parser and template behavior.
func decodeToolCallArguments(toolName, raw string, output *map[string]any) error {
	var method struct {
		Method string `json:"method"`
	}
	if strings.TrimSpace(toolName) != desktopCdpToolName || json.Unmarshal([]byte(raw), &method) != nil ||
		(method.Method != desktopAwcpSnapshotMethod && method.Method != desktopAwcpInvokeMethod) {
		return json.Unmarshal([]byte(raw), output)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder)
	if err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("AWCP arguments contain more than one JSON value")
		}
		return err
	}
	args, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("AWCP arguments must be one JSON object")
	}
	if err := assertSafeAwcpNumbers(args); err != nil {
		return err
	}
	*output = args
	return nil
}

func decodeUniqueJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		out := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("AWCP object key is not a string")
			}
			if _, exists := out[key]; exists {
				return nil, fmt.Errorf("AWCP arguments contain duplicate JSON key %q", key)
			}
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		if closeToken, err := decoder.Token(); err != nil || closeToken != json.Delim('}') {
			return nil, fmt.Errorf("AWCP object is incomplete")
		}
		return out, nil
	case json.Delim('['):
		out := []any{}
		for decoder.More() {
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		if closeToken, err := decoder.Token(); err != nil || closeToken != json.Delim(']') {
			return nil, fmt.Errorf("AWCP array is incomplete")
		}
		return out, nil
	case json.Delim('}'), json.Delim(']'):
		return nil, fmt.Errorf("AWCP JSON has an unexpected closing token")
	default:
		return token, nil
	}
}

func assertSafeAwcpNumbers(value any) error {
	switch typed := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(string(typed), 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > awcpMaxSafeJSONNumber {
			return fmt.Errorf("AWCP JSON number cannot be represented safely by the page")
		}
	case map[string]any:
		for _, child := range typed {
			if err := assertSafeAwcpNumbers(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := assertSafeAwcpNumbers(child); err != nil {
				return err
			}
		}
	}
	return nil
}
