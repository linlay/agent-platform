// Package runenvops exposes the dynamic Run environment as a native tool.
package runenvops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"agent-platform/internal/toolpolicy"
)

const ToolName = "run_env"

type ToolHandler struct{ cfg config.RunEnvConfig }

func NewToolHandler(cfg config.RunEnvConfig) *ToolHandler { return &ToolHandler{cfg: cfg} }
func (*ToolHandler) ToolNames() []string                  { return []string{ToolName} }

func callerAllowed(ctx *contracts.ExecutionContext) bool {
	if ctx == nil || ctx.RunEnvironment == nil {
		return false
	}
	s := ctx.Session
	owner := contracts.ResolveRunOwner(s.RunOwner)
	if strings.TrimSpace(s.RunID) == "" || strings.TrimSpace(s.AgentKey) == "" || s.SubTaskID != "" || owner.AgentKey != s.AgentKey || !slices.Contains(s.ToolNames, ToolName) {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(s.Mode)) {
	case "GENERAL", "REACT", "CODER", "KBASE", "TEAM":
		return true
	}
	return false
}

func (h *ToolHandler) Invoke(_ context.Context, _ string, args map[string]any, ctx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	result := h.invoke(args, ctx)
	return result, nil
}
func (h *ToolHandler) invoke(args map[string]any, ctx *contracts.ExecutionContext) contracts.ToolExecutionResult {
	for key := range args {
		if key != "operation" && key != "params" {
			return invalid("unknown field " + key)
		}
	}
	operation, ok := args["operation"].(string)
	if !ok || strings.TrimSpace(operation) == "" {
		return invalid("operation must be a non-empty string")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	policy, ok := toolpolicy.LookupOperation(ToolName, operation)
	if !ok {
		return failure("run_env_invalid_operation", "unsupported operation")
	}
	if !callerAllowed(ctx) {
		return failure("run_env_unavailable", "run_env requires a mounted ordinary native root Run")
	}
	if !policy.AllowsExecutionPolicy(ctx.ToolExecutionPolicy) {
		return failure("run_env_stage_forbidden", "mutation is unavailable in read-only stages")
	}
	params := map[string]any{}
	if raw, exists := args["params"]; exists {
		params, ok = raw.(map[string]any)
		if !ok {
			return invalid("params must be an object")
		}
	}
	switch operation {
	case "list":
		if len(params) != 0 {
			return invalid("list accepts no parameters")
		}
		data, err := ctx.RunEnvironment.Inspect()
		if err != nil {
			return mutationError(err)
		}
		return success(data)
	case "explain":
		for key := range params {
			if key != "key" {
				return invalid("unknown params field " + key)
			}
		}
		data := map[string]any{
			"normalization":  "trim whitespace, then uppercase",
			"keyPattern":     "^[A-Z_][A-Z0-9_]{0,127}$",
			"precedence":     []string{"inherited host environment", "Agent environment", "Skill environments in order", "Run dynamic environment", "invocation environment", "Platform reserved context"},
			"inherits":       []string{"subsequent Host tool processes", "subsequent Container tool commands"},
			"doesNotInherit": []string{"child Agents", "Teams", "other Runs", "Terminal", "MCP", "ACP", "Proxy", "Channel", "LSP", "sidecar", "already started processes"},
			"persistence":    "process-local; cleared at Run termination; never restored after restart",
			"visibility":     "all arguments and values, including idempotencyKey, are observable; do not use secrets",
			"byteAccounting": "UTF-8 value bytes only; keys excluded",
			"denyKeys":       append([]string(nil), h.cfg.DenyKeys...),
			"protectedKeys":  "Platform reserved variables and unsafe shell/loader/interpreter keys cannot be overridden",
		}
		if raw, exists := params["key"]; exists {
			key, ok := raw.(string)
			if !ok {
				return invalid("key must be a string")
			}
			key = runenv.NormalizeName(key)
			info := map[string]any{"name": key, "allowed": true}
			if err := runenv.ValidateName(key, h.cfg.DenyKeys); err != nil {
				info["allowed"] = false
				info["reason"] = err.Error()
			}
			data["key"] = info
		}
		return success(data)
	}
	request, err := parseMutation(operation, params)
	if err != nil {
		return invalid(err.Error())
	}
	if strings.TrimSpace(ctx.CurrentToolID) == "" {
		return failure("run_env_unavailable", "mutation requires a tool call identity")
	}
	request.DefaultIdempotencyKey = ctx.Session.RunID + ":" + ctx.CurrentToolID
	result, err := ctx.RunEnvironment.Mutate(request)
	if err != nil {
		return mutationError(err)
	}
	data := map[string]any{"revision": result.Revision, "changed": result.Changed, "idempotent": result.Idempotent}
	if operation != "update" {
		data["key"] = result.Key
	}
	return success(data)
}
func parseMutation(operation string, params map[string]any) (runenv.MutationRequest, error) {
	request := runenv.MutationRequest{Operation: runenv.Operation(operation)}
	allowed := map[string]bool{"expectedRevision": true, "idempotencyKey": true}
	if operation == "update" {
		allowed["set"] = true
		allowed["unset"] = true
	} else {
		allowed["key"] = true
		if operation == "set" {
			allowed["value"] = true
		}
	}
	for key := range params {
		if !allowed[key] {
			return request, fmt.Errorf("unknown params field %s", key)
		}
	}
	if raw, exists := params["expectedRevision"]; exists {
		var revision uint64
		switch v := raw.(type) {
		case int:
			if v < 0 {
				return request, fmt.Errorf("expectedRevision must be a non-negative integer")
			}
			revision = uint64(v)
		case int64:
			if v < 0 {
				return request, fmt.Errorf("expectedRevision must be a non-negative integer")
			}
			revision = uint64(v)
		case uint64:
			revision = v
		case json.Number:
			n, e := v.Int64()
			if e != nil || n < 0 {
				return request, fmt.Errorf("invalid expectedRevision")
			}
			revision = uint64(n)
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= 18446744073709551616.0 || math.Trunc(v) != v {
				return request, fmt.Errorf("invalid expectedRevision")
			}
			revision = uint64(v)
		default:
			return request, fmt.Errorf("expectedRevision must be a non-negative integer")
		}
		request.ExpectedRevision = &revision
	}
	if raw, exists := params["idempotencyKey"]; exists {
		key, ok := raw.(string)
		key = strings.TrimSpace(key)
		if !ok || len(key) == 0 || len(key) > 128 {
			return request, fmt.Errorf("idempotencyKey must contain 1 to 128 bytes")
		}
		request.IdempotencyKey = key
	}
	if operation != "update" {
		key, ok := params["key"].(string)
		if !ok {
			return request, fmt.Errorf("key must be a string")
		}
		request.Name = key
		if operation == "set" {
			value, ok := params["value"].(string)
			if !ok {
				return request, fmt.Errorf("value must be a string")
			}
			request.Value = value
		}
		return request, nil
	}
	request.Set = map[string]string{}
	if raw, exists := params["set"]; exists {
		entries, ok := raw.(map[string]any)
		if !ok {
			return request, fmt.Errorf("set must be an object with string values")
		}
		for key, raw := range entries {
			value, ok := raw.(string)
			if !ok {
				return request, fmt.Errorf("set values must be strings")
			}
			request.Set[key] = value
		}
	}
	if raw, exists := params["unset"]; exists {
		entries, ok := raw.([]any)
		if !ok {
			return request, fmt.Errorf("unset must be an array of strings")
		}
		for _, raw := range entries {
			key, ok := raw.(string)
			if !ok {
				return request, fmt.Errorf("unset entries must be strings")
			}
			request.Unset = append(request.Unset, key)
		}
	}
	if len(request.Set) == 0 && len(request.Unset) == 0 {
		return request, fmt.Errorf("update must contain at least one set or unset key")
	}
	return request, nil
}
func success(data map[string]any) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{Structured: data, Output: contracts.CompactToolModelOutput(data, "")}
}
func failure(code, message string) contracts.ToolExecutionResult {
	result := success(map[string]any{"error": code, "message": message})
	result.Error = code
	result.ExitCode = -1
	return result
}
func invalid(message string) contracts.ToolExecutionResult {
	return failure("run_env_invalid_params", message)
}
func mutationError(err error) contracts.ToolExecutionResult {
	code := "run_env_invalid_request"
	switch {
	case errors.Is(err, runenv.ErrClosed):
		code = "run_env_closed"
	case errors.Is(err, runenv.ErrRevisionConflict):
		code = "run_env_revision_conflict"
	case errors.Is(err, runenv.ErrKeyNotSet):
		code = "run_env_key_not_set"
	case errors.Is(err, runenv.ErrIdempotencyConflict):
		code = "run_env_idempotency_conflict"
	case errors.Is(err, runenv.ErrIdempotencyLimit):
		code = "run_env_idempotency_limit"
	case strings.Contains(err.Error(), "name must match"):
		code = "run_env_key_invalid"
	case strings.Contains(err.Error(), "reserved"), strings.Contains(err.Error(), "denied"):
		code = "run_env_key_forbidden"
	case strings.Contains(err.Error(), "value must"), strings.Contains(err.Error(), "value exceeds"):
		code = "run_env_value_invalid"
	case strings.Contains(err.Error(), "dynamic keys"), strings.Contains(err.Error(), "total bytes"):
		code = "run_env_limit_exceeded"
	}
	return failure(code, err.Error())
}
