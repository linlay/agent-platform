package llm

import (
	"fmt"
	"strings"
)

// A binding belongs to the request that exposed these Actions, not to the
// latest discovery at execution time. It is never recovered from chat history.
type awcpRequestBinding struct {
	owner      *awcpRunConstraint
	revision   string
	generation uint64
	actions    []awcpActionConstraint
}

func (s *llmRunStream) awcpRequestTools() ([]openAIToolSpec, *awcpRequestBinding, error) {
	if len(s.toolSpecs) == 0 || s.toolChoice == "none" {
		return s.toolSpecs, nil, nil
	}
	if s.awcpConstraint.stoppedReason != "" {
		out := append([]openAIToolSpec(nil), s.toolSpecs...)
		for index, spec := range out {
			if spec.Function.Name != desktopCdpToolName {
				continue
			}
			parameters := cloneToolSchemaMap(spec.Function.Parameters)
			method := anyMap(anyMap(parameters["properties"])["method"])
			methods, ok := method["enum"].([]any)
			if !ok {
				return nil, nil, fmt.Errorf("desktop_cdp method enum is missing")
			}
			allowed := make([]any, 0, len(methods))
			for _, name := range methods {
				if name != desktopAwcpSnapshotMethod && name != desktopAwcpInvokeMethod {
					allowed = append(allowed, name)
				}
			}
			method["enum"] = allowed
			out[index].Function.Parameters = parameters
		}
		return out, nil, nil
	}
	if s.awcpConstraint.revision == "" {
		return s.toolSpecs, nil, nil
	}
	for index, spec := range s.toolSpecs {
		if spec.Function.Name != desktopCdpToolName {
			continue
		}
		binding := &awcpRequestBinding{
			owner:    &s.awcpConstraint,
			revision: s.awcpConstraint.revision, generation: s.awcpConstraint.generation,
			actions: cloneAwcpActions(s.awcpConstraint.actions),
		}
		if len(binding.actions) == 0 {
			return s.toolSpecs, binding, nil
		}
		parameters := cloneToolSchemaMap(spec.Function.Parameters)
		params := anyMap(anyMap(parameters["properties"])["params"])
		if params == nil {
			return nil, nil, fmt.Errorf("desktop_cdp params schema is missing")
		}
		properties := anyMap(params["properties"])
		if properties == nil {
			properties = map[string]any{}
			params["properties"] = properties
		}
		if _, exists := properties["action"]; exists {
			return nil, nil, fmt.Errorf("desktop_cdp params.action conflicts with AWCP")
		}
		actions := make(map[string]any, len(binding.actions))
		for _, action := range binding.actions {
			prefix := "#/properties/params/properties/action/properties/" + awcpSchemaPointerToken(action.action)
			schema, err := relocateAwcpInputSchema(action.inputSchema, prefix)
			if err != nil {
				return nil, nil, fmt.Errorf("AWCP Action %s inputSchema: %w", action.action, err)
			}
			// Keep the input fields visible at the Action key. Only references
			// already present in the page schema need indirection.
			description := action.description
			if inputDescription, ok := schema["description"].(string); ok && inputDescription != "" && inputDescription != description {
				description += "\n" + inputDescription
			}
			schema["description"] = description
			actions[action.action] = schema
		}
		properties["action"] = map[string]any{
			"type": "object", "properties": actions,
			"minProperties": 1, "maxProperties": 1, "additionalProperties": false,
			"description": "AWCP.invoke only. Supply exactly one discovered Action key with its typed input object as the value. Omit revision and args.",
		}
		out := append([]openAIToolSpec(nil), s.toolSpecs...)
		out[index].Function.Parameters = parameters
		return out, binding, nil
	}
	return s.toolSpecs, nil, nil
}

func awcpSchemaPointerToken(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

// Relocate only schema locations. Descriptions, examples and enum values are
// data, even when they contain an object with a property named "$ref".
func relocateAwcpInputSchema(input map[string]any, prefix string) (map[string]any, error) {
	out := cloneToolSchemaMap(input)
	locations := map[string]bool{}
	refs := []string{}
	var visit func(map[string]any, string, int) error
	visit = func(schema map[string]any, location string, depth int) error {
		if schema == nil || depth > awcpMaxSchemaDepth {
			return fmt.Errorf("missing schema or schema depth exceeded")
		}
		locations[location] = true
		for _, key := range []string{"oneOf", "allOf", "not", "if", "then", "else", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor", "$recursiveRef", "$recursiveAnchor", "dependencies", "dependentSchemas", "unevaluatedProperties", "unevaluatedItems"} {
			if _, exists := schema[key]; exists {
				return fmt.Errorf("unsupported keyword %s", key)
			}
		}
		if raw, exists := schema["$ref"]; exists {
			ref, ok := raw.(string)
			if !ok || (ref != "#" && !strings.HasPrefix(ref, "#/")) {
				return fmt.Errorf("only local JSON pointer references are supported")
			}
			refs = append(refs, ref)
			schema["$ref"] = prefix + strings.TrimPrefix(ref, "#")
		} else {
			switch schema["type"] {
			case "object":
				_, hasProperties := schema["properties"].(map[string]any)
				_, hasBranches := schema["anyOf"].([]any)
				if !hasProperties && !hasBranches {
					return fmt.Errorf("object inputs must declare properties")
				}
			case "array":
				if _, ok := schema["items"].(map[string]any); !ok {
					return fmt.Errorf("array inputs must declare typed items")
				}
			case "string", "number", "integer", "boolean", "null":
			default:
				return fmt.Errorf("every input schema must declare one concrete type or local reference")
			}
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions"} {
			if raw, exists := schema[key]; exists {
				children, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s must be a schema map", key)
				}
				for name, child := range children {
					pointer := awcpSchemaPointerToken(name)
					if err := visit(anyMap(child), location+"/"+key+"/"+pointer, depth+1); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{"items", "contains", "propertyNames", "additionalProperties"} {
			if raw, exists := schema[key]; exists {
				if key == "additionalProperties" {
					if _, ok := raw.(bool); ok {
						continue
					}
				}
				if err := visit(anyMap(raw), location+"/"+key, depth+1); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"anyOf", "prefixItems"} {
			if raw, exists := schema[key]; exists {
				items, ok := raw.([]any)
				if !ok || (key == "anyOf" && len(items) == 0) {
					return fmt.Errorf("%s must be an array of schemas", key)
				}
				for index, item := range items {
					if err := visit(anyMap(item), fmt.Sprintf("%s/%s/%d", location, key, index), depth+1); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := visit(out, "#", 0); err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if !locations[ref] {
			return nil, fmt.Errorf("reference %q does not point to a supported schema location", ref)
		}
	}
	return out, nil
}

// Context estimates include the next request's schema. Assembly errors are
// reported by prepareNextTurn; they must never cause an upstream request.
func (s *llmRunStream) toolSpecsForContextEstimate() []openAIToolSpec {
	specs, _, err := s.awcpRequestTools()
	if err != nil {
		return s.toolSpecs
	}
	return specs
}
