package platformcontrol

import (
	"encoding/json"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
)

const ToolName = "platform_control"

type Descriptor struct {
	Name           string
	RiskClass      string
	ReadOnly       bool
	Barrier        bool
	SensitivePaths []string
	AllowedStages  []string
	Validate       func(map[string]any) error
	Invoke         func(*ToolHandler, string, map[string]any, *contracts.ExecutionContext) contracts.ToolExecutionResult
}

var descriptors = map[string]Descriptor{
	"capabilities.list":    operation("capabilities.list", "low", true, false, nil, "all"),
	"catalog.defaults.get": operation("catalog.defaults.get", "low", true, false, nil, "all"),
	"catalog.validate":     operation("catalog.validate", "low", true, false, []string{"params.content"}, "all"),
	"run.env.set":          operation("run.env.set", "high", false, true, []string{"params.idempotencyKey"}, "main"),
	"run.env.unset":        operation("run.env.unset", "high", false, true, []string{"params.idempotencyKey"}, "main"),
	"runtime.status":       operation("runtime.status", "low", true, false, nil, "all"),
	"security.explain":     operation("security.explain", "low", true, false, nil, "all"),
}

func operation(name, risk string, readOnly, barrier bool, sensitive []string, stage string) Descriptor {
	return Descriptor{
		Name: name, RiskClass: risk, ReadOnly: readOnly, Barrier: barrier, SensitivePaths: sensitive,
		AllowedStages: []string{stage},
	}
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

func (d Descriptor) AllowsExecutionPolicy(policy string) bool {
	stage := "main"
	if strings.EqualFold(strings.TrimSpace(policy), "read_only") {
		stage = "planning"
	}
	for _, allowed := range d.AllowedStages {
		if strings.EqualFold(strings.TrimSpace(allowed), "all") || strings.EqualFold(strings.TrimSpace(allowed), stage) {
			return true
		}
	}
	return false
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
		// generic value field fail-closed, while registered run.env.set values
		// remain ordinary observable tool arguments.
		paths = append(paths, "params.value")
	}
	for _, path := range paths {
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
	value, exists := object[path[0]]
	if !exists {
		return
	}
	if text, ok := value.(string); ok && (path[0] == "value" || path[0] == "content") {
		object[path[0]+"Bytes"] = len([]byte(text))
	}
	object[path[0]] = "[REDACTED]"
}

func stringArg(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
