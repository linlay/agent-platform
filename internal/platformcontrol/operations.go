package platformcontrol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
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
	"run.env.bind":         operation("run.env.bind", "high", false, true, []string{"params.value", "params.idempotencyKey"}, "main"),
	"run.env.set":          operation("run.env.set", "high", false, true, []string{"params.value", "params.idempotencyKey"}, "main"),
	"run.env.unset":        operation("run.env.unset", "high", false, true, []string{"params.idempotencyKey"}, "main"),
	"run.env.get":          operation("run.env.get", "low", true, false, nil, "all"),
	"run.env.list":         operation("run.env.list", "low", true, false, nil, "all"),
	"run.env.bulk":         operation("run.env.bulk", "high", false, true, []string{"params.changes.*.value", "params.idempotencyKey"}, "main"),
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

type ApprovalMetadata struct {
	Required bool
	Display  string
	Keys     []string
}

func MutationApproval(args map[string]any, policy runenv.Policy) ApprovalMetadata {
	descriptor, ok := InvocationDescriptor(ToolName, args)
	if !ok || descriptor.ReadOnly {
		return ApprovalMetadata{}
	}
	params, _ := args["params"].(map[string]any)
	type keyLength struct {
		key    string
		length int
	}
	items := []keyLength{}
	switch descriptor.Name {
	case "run.env.bind", "run.env.set", "run.env.unset":
		value, _ := params["value"].(string)
		items = append(items, keyLength{key: strings.ToUpper(strings.TrimSpace(stringArg(params, "key"))), length: len([]byte(value))})
	case "run.env.bulk":
		changes, _ := params["changes"].([]any)
		for _, raw := range changes {
			change, _ := raw.(map[string]any)
			value, _ := change["value"].(string)
			items = append(items, keyLength{key: strings.ToUpper(strings.TrimSpace(stringArg(change, "key"))), length: len([]byte(value))})
		}
	}
	parts := make([]string, 0, len(items))
	keys := make([]string, 0, len(items))
	required := false
	for _, item := range items {
		keyPolicy, ok := policy.Key(item.key)
		if !ok {
			continue
		}
		keys = append(keys, item.key)
		parts = append(parts, fmt.Sprintf("%s(%d bytes, source=run.dynamic, targets=%s)", item.key, item.length, joinTargets(keyPolicy.Targets)))
		if keyPolicy.Approval == runenv.ApprovalEachChange {
			required = true
		}
	}
	return ApprovalMetadata{Required: required, Display: descriptor.Name + " " + strings.Join(parts, ", "), Keys: keys}
}

func joinTargets(targets []runenv.Target) string {
	items := make([]string, 0, len(targets))
	for _, target := range targets {
		items = append(items, string(target))
	}
	return strings.Join(items, "+")
}

func SanitizeArguments(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return `{"redacted":true}`
	}
	descriptor, known := InvocationDescriptor(ToolName, args)
	failClosedPaths := []string{
		"params.value", "params.content", "params.idempotencyKey", "params.changes.*.value",
	}
	paths := append([]string(nil), failClosedPaths...)
	if known {
		paths = append(append([]string(nil), descriptor.SensitivePaths...), failClosedPaths...)
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
