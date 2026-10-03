package platformcontrol

import (
	"encoding/json"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/toolpolicy"
)

const ToolName = "platform_control"

type Descriptor struct {
	toolpolicy.Operation
	RiskClass      string
	SensitivePaths []string
	Validate       func(map[string]any) error
	Invoke         func(*ToolHandler, string, map[string]any, *contracts.ExecutionContext) contracts.ToolExecutionResult
}

var descriptors = map[string]Descriptor{
	"capabilities.list":    operation("capabilities.list", "low"),
	"catalog.defaults.get": operation("catalog.defaults.get", "low"),
	"catalog.validate":     operation("catalog.validate", "low"),
	"chat.set_pinned":      operation("chat.set_pinned", "low"),
	"runtime.status":       operation("runtime.status", "low"),
	"security.explain":     operation("security.explain", "low"),
}

func operation(name, risk string) Descriptor {
	policy, ok := toolpolicy.LookupOperation(ToolName, name)
	if !ok {
		panic("missing platform operation policy: " + name)
	}
	return Descriptor{Operation: policy, RiskClass: risk}
}

func init() {
	for name, descriptor := range descriptors {
		operationName := name
		descriptor.Validate = func(params map[string]any) error { return validateOperationParams(operationName, params) }
		descriptor.Invoke = func(handler *ToolHandler, requestedOperation string, params map[string]any, execCtx *contracts.ExecutionContext) contracts.ToolExecutionResult {
			return invokeRegisteredOperation(handler, requestedOperation, params, execCtx)
		}
		descriptors[name] = descriptor
	}
}

func LookupOperation(name string) (Descriptor, bool) {
	descriptor, ok := descriptors[strings.ToLower(strings.TrimSpace(name))]
	return descriptor, ok
}

func OperationNames() []string {
	names := make([]string, 0, len(descriptors))
	for name := range descriptors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func InvocationDescriptor(toolName string, args map[string]any) (Descriptor, bool) {
	if !strings.EqualFold(strings.TrimSpace(toolName), ToolName) {
		return Descriptor{}, false
	}
	return LookupOperation(stringArg(args, "operation"))
}

// SanitizeArguments preserves the request shape: display metadata belongs in
// results, never in params that a model may copy into a subsequent request.
// Reapplying this transformation must not change an already sanitized call.
func SanitizeArguments(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return `{"redacted":true}`
	}
	descriptor, known := InvocationDescriptor(ToolName, args)
	failClosedPaths := []string{
		"params.content", "params.idempotencyKey",
	}
	paths := append([]string(nil), failClosedPaths...)
	if known {
		paths = append(append([]string(nil), descriptor.SensitivePaths...), failClosedPaths...)
	} else {
		// Unknown operations have no trusted argument contract. Keep their
		// generic value field fail-closed.
		paths = append(paths, "params.value")
	}
	params, _ := args["params"].(map[string]any)
	// Catalog definitions are ordinary observable tool input. Match the execution
	// handler's resource-type normalization; unknown candidates remain redacted.
	visibleCatalogCandidate := false
	if known && descriptor.Name == "catalog.validate" {
		switch strings.ToLower(strings.TrimSpace(stringArg(params, "resourceType"))) {
		case "agent", "team", "skill", "connector":
			visibleCatalogCandidate = true
		}
	}
	for _, path := range paths {
		if path == "params.content" && visibleCatalogCandidate {
			continue
		}
		redactSensitivePath(args, strings.Split(path, "."))
	}
	rawSanitized, err := json.Marshal(args)
	if err != nil {
		return `{"redacted":true}`
	}
	return string(rawSanitized)
}

func redactSensitivePath(node any, path []string) {
	if len(path) == 0 {
		return
	}
	if path[0] == "*" {
		items, _ := node.([]any)
		for _, item := range items {
			redactSensitivePath(item, path[1:])
		}
		return
	}
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	if len(path) > 1 {
		redactSensitivePath(object[path[0]], path[1:])
		return
	}
	_, exists := object[path[0]]
	if !exists {
		return
	}
	object[path[0]] = "[REDACTED]"
}

func stringArg(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
