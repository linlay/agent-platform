package connectorops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
	"github.com/google/jsonschema-go/jsonschema"
)

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string              { return e.Code }
func failure(code string, status int) error { return &Error{code, status} }

type Scope struct {
	Subject string
	AppID   string
	// Allowed operations are frozen by a trusted grant issuer, not request JSON.
	Chats      map[string]bool
	Operations map[string][]string
	Check      func() error
}
type Request struct {
	ConnectorID string         `json:"connectorId"`
	OperationID string         `json:"operationId"`
	Revision    string         `json:"revision"`
	Arguments   map[string]any `json:"arguments"`
}
type Result struct {
	InvocationID string         `json:"invocationId"`
	ConnectorID  string         `json:"connectorId"`
	OperationID  string         `json:"operationId"`
	Revision     string         `json:"revision"`
	Status       string         `json:"status"`
	Output       map[string]any `json:"output"`
}
type Service struct {
	Auth    *connectorauth.Manager
	Sources connector.Sources
}

func (s Scope) permits(connectorID, operationID string) bool {
	if s.Subject == "" || s.AppID == "" || s.Check == nil || s.Check() != nil {
		return false
	}
	for _, id := range s.Operations[connectorID] {
		if id == operationID {
			return true
		}
	}
	return false
}
func (s *Service) Describe(scope Scope, id string) (Catalog, error) {
	allowed := scope.Operations[id]
	if len(allowed) == 0 || !scope.permits(id, allowed[0]) {
		return Catalog{}, failure("operation_not_allowed", 403)
	}
	pkg, err := s.Sources.Load(id)
	if err != nil {
		return Catalog{}, failure("connector_unavailable", 503)
	}
	catalog, err := Load(pkg)
	if err != nil {
		return Catalog{}, failure("connector_unavailable", 503)
	}
	filtered := make([]Operation, 0, len(catalog.Operations))
	for _, op := range catalog.Operations {
		if scope.permits(id, op.ID) {
			filtered = append(filtered, op)
		}
	}
	catalog.Operations = filtered
	return catalog, nil
}
func (s *Service) Invoke(ctx context.Context, scope Scope, req Request) (Result, error) {
	result := Result{}
	if !scope.permits(req.ConnectorID, req.OperationID) {
		return result, failure("operation_not_allowed", 403)
	}
	catalog, err := s.Describe(scope, req.ConnectorID)
	if err != nil {
		return result, err
	}
	if req.Revision == "" || req.Revision != catalog.Revision {
		return result, failure("operation_revision_mismatch", 409)
	}
	var selected *Operation
	for i := range catalog.Operations {
		if catalog.Operations[i].ID == req.OperationID {
			selected = &catalog.Operations[i]
			break
		}
	}
	if selected == nil {
		return result, failure("operation_not_allowed", 403)
	}
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}
	encoded, err := json.Marshal(req.Arguments)
	if err != nil || len(encoded) > MaxJSONBytes || validateValue(selected.input, encoded) != nil {
		return result, failure("invalid_arguments", 400)
	}
	auth := s.Auth
	if auth == nil {
		return result, failure("connector_unavailable", 503)
	}
	pkg, err := s.Sources.Load(req.ConnectorID)
	if err != nil {
		return result, failure("connector_unavailable", 503)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	component := ""
	if selected.MCP != nil {
		component = selected.MCP.Component
	}
	status, err := auth.StatusComponent(ctx, req.ConnectorID, component)
	if err != nil || status.Status == "setup_required" {
		return result, failure("connector_unavailable", 503)
	}
	if status.Status != "authorized" {
		return result, failure("connector_auth_required", 401)
	}
	// Keep the package stable through dispatch, including cross-process imports.
	if !pkg.Builtin {
		release, lockErr := connector.AcquireOperation(s.Sources.ExternalRoot, req.ConnectorID)
		if lockErr != nil {
			return result, failure("connector_busy", 409)
		}
		defer release()
	}
	pkg, err = s.Sources.Load(req.ConnectorID)
	if err != nil {
		return result, failure("connector_unavailable", 503)
	}
	revision, err := auth.Revision(req.ConnectorID)
	if err != nil {
		return result, failure("connector_auth_required", 401)
	}
	// Credential resolution and login checks can block; recheck grants and package
	// revision immediately before starting the actual business operation.
	fresh, err := Load(pkg)
	if err != nil || fresh.Revision != catalog.Revision {
		return result, failure("operation_revision_mismatch", 409)
	}
	if !scope.permits(req.ConnectorID, req.OperationID) {
		return result, failure("operation_not_allowed", 403)
	}
	var output map[string]any
	if selected.CLI != nil {
		output, err = callCLI(ctx, pkg, *selected.CLI, encoded)
	} else {
		output, err = callMCP(ctx, pkg, *selected.MCP, req.Arguments)
	}
	if ctx.Err() != nil {
		return result, failure("invocation_timeout", 504)
	}
	if err != nil {
		return result, err
	}
	after, err := auth.Revision(req.ConnectorID)
	if err != nil || after != revision {
		return result, failure("connector_auth_expired", 401)
	}
	if !scope.permits(req.ConnectorID, req.OperationID) {
		return result, failure("operation_not_allowed", 403)
	}
	outputJSON, _ := json.Marshal(output)
	if validateValue(selected.output, outputJSON) != nil {
		return result, failure("invalid_upstream_response", 502)
	}
	return Result{InvocationID: rand.Text(), ConnectorID: req.ConnectorID, OperationID: req.OperationID, Revision: req.Revision, Status: "succeeded", Output: output}, nil
}

type boundedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	available := MaxJSONBytes - b.b.Len()
	if len(p) > available {
		p = p[:available]
		b.overflow = true
	}
	b.b.Write(p)
	return n, nil
}
func callCLI(ctx context.Context, pkg connector.Package, op CLI, args []byte) (map[string]any, error) {
	entry, err := Entry(pkg, op.Entry)
	if err != nil {
		return nil, failure("connector_unavailable", 503)
	}
	argv := append(append([]string{}, op.Args...), op.JSONFlag, string(args))
	cmd := exec.CommandContext(ctx, entry, argv...)
	configureProcess(cmd)
	cmd.Dir = pkg.Dir
	// Deliberate allowlist: never inherit AP_ACCESS_TOKEN, another connector's
	// configEnv, cloud SDK credentials, or process-wide proxy passwords.
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	values, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return nil, failure("connector_unavailable", 503)
	}
	for key, value := range values {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	binding, err := connectorauth.CLIEnvironment(pkg)
	if err != nil {
		return nil, failure("connector_auth_required", 401)
	}
	credentials, err := connectorauth.ResolveEnvironment(ctx, binding, "")
	if err != nil {
		return nil, failure("connector_auth_required", 401)
	}
	for key, value := range credentials {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var out, diagnostic boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		return nil, failure("connector_call_failed", 502)
	}
	if out.overflow {
		return nil, failure("invalid_upstream_response", 502)
	}
	return decodeOutput(out.b.Bytes())
}
func decodeOutput(data []byte) (map[string]any, error) {
	if len(data) > MaxJSONBytes {
		return nil, failure("invalid_upstream_response", 502)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var output map[string]any
	if d.Decode(&output) != nil || output == nil || d.Decode(new(any)) != io.EOF {
		return nil, failure("invalid_upstream_response", 502)
	}
	return output, nil
}
func callMCP(ctx context.Context, pkg connector.Package, op MCP, args map[string]any) (map[string]any, error) {
	client, key, err := mcp.NewOperationClient(pkg, op.Component, op.Tool)
	if err != nil {
		return nil, failure("connector_unavailable", 503)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, key)
	if err != nil {
		return nil, failure("connector_unavailable", 503)
	}
	found := false
	for _, tool := range tools {
		if name, _ := tool.Meta["mcpToolName"].(string); name == op.Tool || tool.Name == op.Tool {
			found = true
		}
	}
	if !found {
		return nil, failure("operation_not_allowed", 403)
	}
	raw, err := client.CallTool(ctx, key, op.Tool, args, nil)
	if err != nil {
		return nil, failure("connector_call_failed", 502)
	}
	data, err := json.Marshal(raw)
	if err != nil || len(data) > MaxJSONBytes {
		return nil, failure("invalid_upstream_response", 502)
	}
	var result struct {
		IsError    bool           `json:"isError"`
		Structured map[string]any `json:"structuredContent"`
	}
	if json.Unmarshal(data, &result) != nil || result.IsError || result.Structured == nil {
		return nil, failure("invalid_upstream_response", 502)
	}
	return result.Structured, nil
}

// The pinned validator treats json.Number as a string in its type checker.
// Validate a standard JSON projection while preserving original numeric bytes
// for argv. Provider identifiers must be declared as strings.
func validateValue(schema *jsonschema.Resolved, data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return schema.Validate(value)
}
