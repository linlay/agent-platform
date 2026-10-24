// Package resultsubmit owns the result_submit tool: the caller's output format,
// the per-Run tool definition built from it, and the checks a submission must
// pass before it is accepted. It is mode neutral; any native Agent that
// mounts result_submit can deliver a result this way.
package resultsubmit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

const ToolName = contracts.ResultSubmitToolName

// MaxOutputSchemaBytes bounds the caller's output format, which is sent to the
// model with every turn of the Run.
const MaxOutputSchemaBytes = 64 << 10

// MaxInvalidSubmissions is how many rejected submissions a Run started through
// /api/execute may make before it fails with output_invalid.
const MaxInvalidSubmissions = 3

// CompileOutputSchema checks the caller's output format: a JSON Schema whose
// root is an object, which the validator can fully resolve. Any keyword it
// cannot honor fails here instead of being ignored at submission time.
func CompileOutputSchema(schema map[string]any) (*jsonschema.Resolved, error) {
	if len(schema) == 0 {
		return nil, errors.New("outputSchema is required")
	}
	if rootType, _ := schema["type"].(string); rootType != "object" {
		return nil, errors.New(`outputSchema must have type "object" at its root`)
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("outputSchema: %w", err)
	}
	if len(data) > MaxOutputSchemaBytes {
		return nil, fmt.Errorf("outputSchema exceeds %d bytes", MaxOutputSchemaBytes)
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("outputSchema: %w", err)
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("outputSchema: %w", err)
	}
	return resolved, nil
}

// BuildToolDefinition clones the embedded result_submit definition and
// replaces its output property with the caller's output format.
func BuildToolDefinition(base api.ToolDetailResponse, outputSchema map[string]any) (api.ToolDetailResponse, error) {
	if !contracts.IsResultSubmitToolName(base.Name) && !contracts.IsResultSubmitToolName(base.Key) {
		return api.ToolDetailResponse{}, fmt.Errorf("embedded tool %q is unavailable", ToolName)
	}
	// The embedded definition is shared by every Run; only deep copies change.
	definition := base
	definition.Parameters = deepCloneMap(base.Parameters)
	definition.OutputSchema = deepCloneMap(base.OutputSchema)
	definition.Meta = deepCloneMap(base.Meta)
	properties, ok := definition.Parameters["properties"].(map[string]any)
	if !ok {
		return api.ToolDetailResponse{}, errors.New("embedded result_submit schema is missing properties")
	}
	baseOutput, _ := properties["output"].(map[string]any)
	output := deepCloneMap(outputSchema)
	description := strings.TrimSpace(contracts.AnyStringNode(baseOutput["description"]))
	if own := strings.TrimSpace(contracts.AnyStringNode(output["description"])); own != "" {
		description = strings.TrimSpace(description + " " + own)
	}
	if description != "" {
		output["description"] = description
	}
	properties["output"] = output
	return definition, nil
}
