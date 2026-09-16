package awcp

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

const MaxSchemaDepth = 20
const MaxSnapshotBytes = 256 * 1024

var actionPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

type Action struct {
	Name         string
	Description  string
	InputSchema  map[string]any
	Example      map[string]any
	OutputSchema map[string]any
}

// ParseSnapshot is the single Platform acceptance rule for the Desktop AWCP
// contract. A failed descriptor rejects the entire discovery response.
func ParseSnapshot(snapshot map[string]any) (string, []Action, error) {
	if !exactKeys(snapshot, "actions", "revision") {
		return "", nil, fmt.Errorf("invalid AWCP snapshot fields")
	}
	revision, ok := snapshot["revision"].(string)
	if !ok || revision == "" || utf16Length(revision) > 128 {
		return "", nil, fmt.Errorf("invalid AWCP revision")
	}
	rawActions, ok := snapshot["actions"].([]any)
	if !ok || len(rawActions) > 128 {
		return "", nil, fmt.Errorf("invalid AWCP Action list")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > MaxSnapshotBytes {
		return "", nil, fmt.Errorf("AWCP snapshot exceeds JSON boundary")
	}
	actions := make([]Action, 0, len(rawActions))
	previous := ""
	for index, raw := range rawActions {
		descriptor, ok := raw.(map[string]any)
		if !ok || (!exactKeys(descriptor, "action", "description", "example", "inputSchema") &&
			!exactKeys(descriptor, "action", "description", "example", "inputSchema", "outputSchema")) {
			return "", nil, fmt.Errorf("AWCP Action at index %d has invalid descriptor fields", index)
		}
		name, nameOK := descriptor["action"].(string)
		description, descriptionOK := descriptor["description"].(string)
		input, inputOK := descriptor["inputSchema"].(map[string]any)
		example, exampleOK := descriptor["example"].(map[string]any)
		if !nameOK || name == "" || utf16Length(name) > 128 || !actionPattern.MatchString(name) || name <= previous {
			return "", nil, fmt.Errorf("AWCP Action at index %d has invalid name or order", index)
		}
		if !descriptionOK || strings.TrimSpace(description) == "" || utf16Length(description) > 2048 {
			return "", nil, fmt.Errorf("AWCP Action %s has invalid description", name)
		}
		if !inputOK || input == nil || !validJSONTree(input, 0, MaxSchemaDepth) {
			return "", nil, fmt.Errorf("AWCP Action %s has invalid inputSchema", name)
		}
		if !exampleOK || example == nil || !validJSONTree(example, 0, MaxSnapshotBytes) {
			return "", nil, fmt.Errorf("AWCP Action %s has invalid example", name)
		}
		action := Action{Name: name, Description: description, InputSchema: input, Example: example}
		if output, present := descriptor["outputSchema"]; present {
			value, ok := output.(map[string]any)
			if !ok || value == nil || !validJSONTree(value, 0, MaxSchemaDepth) {
				return "", nil, fmt.Errorf("AWCP Action %s has invalid outputSchema", name)
			}
			action.OutputSchema = value
		}
		previous = name
		actions = append(actions, action)
	}
	return revision, actions, nil
}

func exactKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	for index, key := range actual {
		if key != want[index] {
			return false
		}
	}
	return true
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func validJSONTree(value any, depth, maxDepth int) bool {
	if depth > maxDepth {
		return false
	}
	switch typed := value.(type) {
	case nil, bool, string,
		int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return !math.IsNaN(float64(typed)) && !math.IsInf(float64(typed), 0)
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case []any:
		for _, item := range typed {
			if !validJSONTree(item, depth+1, maxDepth) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !validJSONTree(item, depth+1, maxDepth) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
