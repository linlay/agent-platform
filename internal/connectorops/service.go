package connectorops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
)

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string              { return e.Code }
func failure(code string, status int) error { return &Error{code, status} }

type Scope struct {
	Subject   string
	AppID     string
	Chats     map[string]bool
	Execution []Permission
	Check     func() error
}
type Request struct {
	ConnectorID        string         `json:"connectorId"`
	Adapter            string         `json:"adapter"`
	Args               []string       `json:"args,omitempty"`
	Component          string         `json:"component,omitempty"`
	ToolName           string         `json:"toolName,omitempty"`
	Arguments          map[string]any `json:"arguments,omitempty"`
	CredentialRevision string         `json:"credentialRevision,omitempty"`
	IdempotencyKey     string         `json:"idempotencyKey,omitempty"`
}
type Result struct {
	CredentialRevision string          `json:"credentialRevision"`
	InvocationID       string          `json:"invocationId"`
	ConnectorID        string          `json:"connectorId"`
	Adapter            string          `json:"adapter"`
	ExitCode           int             `json:"exitCode"`
	Stdout             string          `json:"stdout,omitempty"`
	Stderr             string          `json:"stderr,omitempty"`
	MCP                json.RawMessage `json:"mcp,omitempty"`
}
type Service struct {
	Auth    *connectorauth.Manager
	Sources connector.Sources
}

func (s Scope) permits(id, adapter string) bool {
	if s.Subject == "" || s.AppID == "" || s.Check == nil || s.Check() != nil {
		return false
	}
	for _, p := range s.Execution {
		if p.ConnectorID == id && (adapter == "" || p.Adapter == adapter) {
			return true
		}
	}
	return false
}
func (s *Service) Describe(scope Scope, id string) (Catalog, error) {
	if !scope.permits(id, "") {
		return Catalog{}, failure("connector_execution_not_allowed", 403)
	}
	pkg, err := s.Sources.Load(id)
	if err != nil {
		return Catalog{}, failure("connector_unavailable", 503)
	}
	c, err := Load(pkg)
	if err != nil {
		return c, failure("connector_unavailable", 503)
	}
	allowed := []string{}
	for _, a := range c.Adapters {
		if scope.permits(id, a) {
			allowed = append(allowed, a)
		}
	}
	c.Adapters = allowed
	if !scope.permits(id, "mcp") {
		c.Components = []string{}
	}
	return c, nil
}
func validateRequest(req Request) error {
	if !connector.ValidID(req.ConnectorID) || len(req.CredentialRevision) > 256 {
		return failure("invalid_arguments", 400)
	}
	if req.IdempotencyKey != "" && !idempotencyKeyPattern.MatchString(req.IdempotencyKey) {
		return failure("invalid_arguments", 400)
	}
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > MaxJSONBytes {
		return failure("invalid_arguments", 400)
	}
	switch req.Adapter {
	case "cli":
		if req.Args == nil || len(req.Args) > 256 || req.Component != "" || req.ToolName != "" || req.Arguments != nil {
			return failure("invalid_arguments", 400)
		}
		for _, arg := range req.Args {
			if strings.ContainsRune(arg, 0) {
				return failure("invalid_arguments", 400)
			}
		}
	case "mcp":
		if req.Args != nil || req.Component == "" || len(req.Component) > 256 || req.ToolName == "" || len(req.ToolName) > 256 || req.Arguments == nil {
			return failure("invalid_arguments", 400)
		}
	default:
		return failure("invalid_arguments", 400)
	}
	return nil
}
func (s *Service) Invoke(ctx context.Context, scope Scope, req Request) (result Result, returnErr error) {
	if err := validateRequest(req); err != nil {
		return result, err
	}
	if !scope.permits(req.ConnectorID, req.Adapter) {
		return result, failure("connector_execution_not_allowed", 403)
	}
	catalog, err := s.Describe(scope, req.ConnectorID)
	if err != nil {
		return result, err
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
	if req.Adapter == "mcp" {
		component = req.Component
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
	if req.CredentialRevision != "" && req.CredentialRevision != revision {
		return result, failure("connector_auth_expired", 401)
	}
	// Credential resolution and login checks can block; recheck grants and package
	// revision immediately before starting the actual business operation.
	fresh, err := Load(pkg)
	if err != nil || fresh.Revision != catalog.Revision {
		return result, failure("connector_changed", 409)
	}
	if !scope.permits(req.ConnectorID, req.Adapter) {
		return result, failure("connector_execution_not_allowed", 403)
	}
	var receiptFile string
	if req.IdempotencyKey != "" {
		var previous *Result
		receiptFile, previous, err = beginWrite(s.Sources.PersistentRoot(), scope, req)
		if err != nil {
			return result, err
		}
		if previous != nil {
			return *previous, nil
		}
		defer func() {
			if returnErr != nil {
				returnErr = failure("invocation_outcome_unknown", 409)
			}
		}()
	}
	if req.Adapter == "cli" {
		result, err = callCLI(ctx, pkg, req.Args)
	} else {
		result.MCP, err = callMCP(ctx, pkg, req.Component, req.ToolName, req.Arguments)
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
	if !scope.permits(req.ConnectorID, req.Adapter) {
		return result, failure("connector_execution_not_allowed", 403)
	}
	result.CredentialRevision = revision
	result.InvocationID = rand.Text()
	result.ConnectorID = req.ConnectorID
	result.Adapter = req.Adapter
	wire, encodeErr := json.Marshal(result)
	if encodeErr != nil || len(wire) > MaxJSONBytes-1024 {
		return Result{}, failure("invocation_output_limit", 502)
	}
	if receiptFile != "" {
		if err := finishWrite(receiptFile, req, result); err != nil {
			return Result{}, err
		}
	}
	return result, nil
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
func callCLI(ctx context.Context, pkg connector.Package, args []string) (Result, error) {
	var result Result
	env := []string{}
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	values, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return result, failure("connector_unavailable", 503)
	}
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	binding, err := connectorauth.CLIEnvironment(pkg)
	if err != nil {
		return result, failure("connector_auth_required", 401)
	}
	credentials, err := connectorauth.ResolveEnvironment(ctx, binding, "")
	if err != nil {
		return result, failure("connector_auth_required", 401)
	}
	for k, v := range credentials {
		env = append(env, k+"="+v)
	}
	cmd, err := connectorauth.ExecutionCommand(ctx, pkg, args, env)
	if err != nil {
		return result, failure("connector_cli_unavailable", 503)
	}
	var out, diagnostic boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if out.overflow || diagnostic.overflow {
		return result, failure("invocation_output_limit", 502)
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return result, failure("connector_call_failed", 502)
		}
		result.ExitCode = exit.ExitCode()
	}
	result.Stdout = out.b.String()
	result.Stderr = diagnostic.b.String()
	return result, nil
}
func callMCP(ctx context.Context, pkg connector.Package, component, toolName string, args map[string]any) (json.RawMessage, error) {
	client, key, err := mcp.NewOperationClient(pkg, component, toolName)
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
		if name, _ := tool.Meta["mcpToolName"].(string); name == toolName || tool.Name == toolName {
			found = true
		}
	}
	if !found {
		return nil, failure("connector_execution_not_allowed", 403)
	}
	raw, err := client.CallTool(ctx, key, toolName, args, nil)
	if err != nil {
		return nil, failure("connector_call_failed", 502)
	}
	data, err := json.Marshal(raw)
	if err != nil || len(data) > MaxJSONBytes {
		return nil, failure("invocation_output_limit", 502)
	}
	return data, nil
}
