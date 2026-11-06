package acp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorops"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/toolpolicy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type platformToolsKey struct{}

// The MCP connection belongs to the ACP process; authority belongs to one Run.
// Fixed discovery tools avoid caching a previous Run's connector selection.
type platformTools struct {
	mu         sync.Mutex // RunControl has a single awaiting slot; serialize tool reviews.
	executor   contracts.ToolExecutor
	connectors *connectorops.Service
	session    contracts.QuerySession
	request    runtimetypes.QueryCommand
	emit       func(contracts.AgentDelta)
}

func startPlatformMCP(e *session, process *sessionProcess) (map[string]any, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := "Bearer " + rand.Text()
	server := mcp.NewServer(&mcp.Implementation{Name: "platform", Version: "1"}, nil)
	for _, name := range []string{"platform_tools", "platform_call", "platform_connector"} {
		schema := map[string]any{"type": "object"}
		description := "List tools and input schemas mounted on the current Platform project."
		if name == "platform_call" {
			description = "Call a mounted Platform tool. Use platform_tools to discover names and input schemas."
			schema["properties"] = map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}
			schema["required"] = []string{"name", "arguments"}
		}
		if name == "platform_connector" {
			description = "Execute the installed CLI of a mounted connector, with argv (no shell). Use platform_tools to discover connector IDs."
			schema["properties"] = map[string]any{"connectorId": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}
			schema["required"] = []string{"connectorId", "args"}
		}

		server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			e.mu.Lock()
			turn := e.turnCtx
			var tools *platformTools
			if turn != nil {
				tools, _ = turn.Value(platformToolsKey{}).(*platformTools)
			}
			if tools == nil || turn.Err() != nil {
				e.mu.Unlock()
				return mcpToolResult(nil, fmt.Errorf("no active Platform Run")), nil
			}
			e.turnCalls.Add(1)
			e.mu.Unlock()
			defer e.turnCalls.Done()
			callCtx, cancel := context.WithCancel(turn)
			stop := context.AfterFunc(ctx, cancel)
			defer stop()
			defer cancel()
			value, err := tools.call(callCtx, req.Params.Name, req.Params.Arguments)
			return mcpToolResult(value, err), nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		handler.ServeHTTP(w, r)
	})}
	go httpServer.Serve(listener)
	go func() { <-process.Done(); httpServer.Close() }()
	return map[string]any{"type": "http", "name": "platform", "url": "http://" + listener.Addr().String() + "/mcp", "headers": []map[string]string{{"name": "Authorization", "value": token}}}, nil
}

func mcpToolResult(value any, err error) *mcp.CallToolResult {
	text := ""
	failed := err != nil
	if err != nil {
		text = err.Error()
	} else {
		switch result := value.(type) {
		case contracts.ToolExecutionResult:
			// Follow the existing model-output contract; internal traces and raw
			// business payloads are not model input.
			text = result.Output
			failed = result.Error != "" || result.ExitCode != 0
			if text == "" {
				text = result.Error
			}
		case connectorops.Result:
			text = result.Stdout
			if result.Stderr != "" {
				text += "\n" + result.Stderr
			}
			failed = result.ExitCode != 0
		default:
			data, marshalErr := json.Marshal(value)
			text = string(data)
			if marshalErr != nil {
				text, failed = marshalErr.Error(), true
			}
		}
	}
	return &mcp.CallToolResult{IsError: failed, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (p *platformTools) call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method == "platform_connector" {
		return p.callConnector(ctx, raw)
	}
	var definitions []api.ToolDetailResponse
	if p.executor != nil {
		definitions = p.executor.Definitions()
	}
	if method == "platform_tools" {
		visible := definitions[:0:0]
		for _, def := range definitions {
			if p.session.AllowsTool(def.Name) {
				visible = append(visible, def)
			}
		}
		return map[string]any{"tools": visible, "cliConnectors": p.cliConnectors(ctx)}, nil
	}
	var args struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if json.Unmarshal(raw, &args) != nil || !p.session.AllowsTool(args.Name) {
		return nil, fmt.Errorf("tool is not mounted on this Run")
	}
	for _, def := range definitions {
		if def.Name != args.Name {
			continue
		}
		id := make([]byte, 16)
		rand.Read(id)
		exec := &contracts.ExecutionContext{Session: p.session, RunControl: contracts.RunControlFromContext(ctx), CurrentToolID: hex.EncodeToString(id), CurrentToolName: args.Name, AccessLevel: p.session.AccessLevel}
		// Business reviews can read an input file only after the usual file-access
		// gate. This bridge cannot grant that access implicitly.
		if planner, ok := p.executor.(contracts.ToolApprovalInputPlanner); ok {
			path, err := planner.ToolApprovalInputPath(args.Name, args.Arguments, exec)
			if err != nil {
				return nil, err
			}
			if path != "" {
				return nil, fmt.Errorf("capability_unsupported: tool requires a file-access review")
			}
		}
		var approval *contracts.ToolApproval
		if planner, ok := p.executor.(contracts.ToolApprovalPlanner); ok {
			var err error
			approval, err = planner.PrepareToolApproval(ctx, args.Name, args.Arguments, exec)
			if err != nil {
				return nil, err
			}
		}
		if approval != nil || !toolpolicy.AllowsReadOnly(def, true) {
			title := "Platform: " + args.Name
			if approval != nil {
				title = approval.Title
			}
			if err := p.approve(ctx, title, args.Arguments, approval); err != nil {
				return nil, err
			}
			if approval != nil {
				exec.ToolApprovals = map[string]bool{approval.Fingerprint: true}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return p.executor.Invoke(ctx, args.Name, args.Arguments, exec)
	}
	return nil, fmt.Errorf("tool unavailable")
}

func (p *platformTools) approve(ctx context.Context, title string, args map[string]any, approval *contracts.ToolApproval) error {
	params, _ := json.Marshal(map[string]any{"toolCall": map[string]any{"title": title, "rawInput": args, "review": approval}, "options": []map[string]string{{"optionId": "once", "name": "Allow once", "kind": "allow_once"}, {"optionId": "reject", "name": "Reject", "kind": "reject_once"}}})
	result, err := externalInteraction(ctx, Incoming{Method: "session/request_permission", Params: params}, p.request, p.session, p.emit)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	var reply struct {
		Outcome struct{ Outcome, OptionID string }
	}
	json.Unmarshal(encoded, &reply)
	if reply.Outcome.Outcome != "selected" || reply.Outcome.OptionID != "once" {
		return fmt.Errorf("approval_rejected")
	}
	return nil
}

func (p *platformTools) cliConnectors(ctx context.Context) []string {
	ids := []string{}
	if p.connectors != nil {
		for id := range p.session.ConnectorDirs {
			scope, err := p.connectorScope(ctx, id)
			if err != nil {
				continue
			}
			info, err := p.connectors.Describe(scope, id)
			if err == nil {
				for _, a := range info.Adapters {
					if a == "cli" {
						ids = append(ids, id)
					}
				}
			}
		}
	}
	sort.Strings(ids)
	return ids
}
func (p *platformTools) connectorScope(ctx context.Context, id string) (connectorops.Scope, error) {
	dir := p.session.ConnectorDirs[id]
	if dir == "" || p.connectors == nil {
		return connectorops.Scope{}, fmt.Errorf("connector_not_mounted")
	}
	fingerprint, err := connector.RuntimeFingerprint(dir)
	if err != nil {
		return connectorops.Scope{}, err
	}
	subject := p.session.Subject
	if subject == "" {
		subject = "run:" + p.session.RunID
	}
	scope := connectorops.Scope{Subject: subject, IdempotencyNamespace: p.session.RunID, Execution: []connectorops.Permission{{ConnectorID: id, Adapter: "cli"}}, Check: func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pkg, err := p.connectors.Sources.Load(id)
		if err != nil {
			return err
		}
		current, err := connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			return err
		}
		if current != fingerprint {
			return fmt.Errorf("connector_changed")
		}
		return nil
	}}
	return scope, nil
}
func (p *platformTools) callConnector(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		ConnectorID string   `json:"connectorId"`
		Args        []string `json:"args"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Args == nil {
		return nil, fmt.Errorf("invalid_arguments")
	}
	scope, err := p.connectorScope(ctx, args.ConnectorID)
	if err != nil {
		return nil, err
	}
	if err := scope.Check(); err != nil {
		return nil, err
	}
	// The ACP engine owns confirmation of this MCP call according to its mode.
	// Platform still enforces the Run's mounts and connector execution authority.
	return p.connectors.Invoke(ctx, scope, connectorops.Request{ConnectorID: args.ConnectorID, Adapter: "cli", Args: args.Args})
}
