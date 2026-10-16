package platformcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"agent-platform/internal/mcp"
	"agent-platform/internal/runtimeskills"
)

type fixtureConnectorChecker struct{ calls int }

func (c *fixtureConnectorChecker) CheckConnector(ctx context.Context, id, component string) (mcp.ConnectorCheckResult, error) {
	c.calls++
	return mcp.ConnectorCheckResult{ConnectorID: id, Component: component, Status: "passed", ProtocolVersion: mcp.ProtocolVersion}, nil
}

type unavailableConnectionReader struct{}

func (unavailableConnectionReader) Connection(context.Context, string) (connectorauth.Connection, error) {
	return connectorauth.Connection{}, errors.New("private-credential-error")
}

func connectorControlFixture(t *testing.T) *ToolHandler {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), SkillsCenterDir: filepath.Join(root, "skills"), ConnectorsCenterDir: filepath.Join(root, "connectors"), StateDir: filepath.Join(root, ".state")}}
	t.Cleanup(func() {
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUAgentsDir())
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUSkillsDir())
	})
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	sources := &adminsource.ControlService{Config: cfg, Registry: registry, Mutations: adminsource.NewService()}
	return NewToolHandler(cfg, registry, nil).ConfigureControl(sources, nil, nil).ConfigureConnectors(connectorauth.New(t.Context(), cfg.Paths.ConnectorSources(), nil), mcp.NewConnectorChecker(cfg.Paths.ConnectorSources()))
}

func TestConnectorCreationAndReadbackUseLocalAuthorizationSnapshot(t *testing.T) {
	for _, mode := range []string{"", "none"} {
		t.Run(mode, func(t *testing.T) {
			h := connectorControlFixture(t)
			checker := &fixtureConnectorChecker{}
			h.connectorChecker = checker
			content := `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp"}`
			if mode != "" {
				content = `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp","authMode":"none"}`
			}
			params := map[string]any{"resourceType": "connector", "resourceKey": "remote", "mcpUrl": "https://example.test/mcp", "content": content}
			execution := controlExecution()
			validation, err := h.Invoke(t.Context(), "catalog_query", map[string]any{"action": "validate", "args": params}, execution)
			if err != nil || validation.Error != "" || validation.Structured["valid"] != true {
				t.Fatalf("URL candidate validation: %+v, %v", validation, err)
			}
			args := map[string]any{"action": "apply", "args": params}
			approval, err := h.PrepareToolApproval(t.Context(), "catalog_manage", args, execution)
			if err != nil {
				t.Fatal(err)
			}
			execution.ToolApprovals = map[string]bool{approval.Fingerprint: true}
			applied, err := h.Invoke(t.Context(), "catalog_manage", args, execution)
			if err != nil || applied.Error != "" || applied.Structured["status"] != "applied" {
				t.Fatalf("creation: %+v, %v", applied, err)
			}
			read, err := h.Invoke(t.Context(), "catalog_query", map[string]any{"action": "get", "args": map[string]any{"resourceType": "connector", "resourceKey": "remote"}}, execution)
			if err != nil || read.Error != "" || checker.calls != 0 {
				t.Fatalf("read contacted remote: %+v, %v", read, err)
			}
			for _, result := range []map[string]any{applied.Structured, read.Structured} {
				next := result["nextAction"].(map[string]any)
				connection := result["connection"].(map[string]any)
				if mode == "" {
					if result["authMode"] != "mcp" || next["action"] != "authorize" || next["route"] != "/connectors/remote" || connection["authentication"].(map[string]any)["status"] != "unauthorized" {
						t.Fatalf("OAuth guidance: %#v", result)
					}
				} else if result["authMode"] != "none" || next["action"] != "checkConnector" {
					t.Fatalf("no_auth should lead to a check: %#v", result)
				}
			}
			checked, err := h.Invoke(t.Context(), "platform_inspect", map[string]any{"action": "checkConnector", "args": map[string]any{"connectorId": "remote", "component": "main"}}, execution)
			if err != nil || checked.Error != "" || checked.Structured["status"] != "passed" || checker.calls != 1 || execution.Session.ConnectorDirs["remote"] != "" {
				t.Fatalf("unmounted check: %+v, %v", checked, err)
			}
		})
	}
}

func TestConnectorSnapshotFailureDoesNotUndoCreation(t *testing.T) {
	h := connectorControlFixture(t)
	h.connectorConnections = unavailableConnectionReader{}
	execution := controlExecution()
	args := map[string]any{"action": "apply", "args": map[string]any{"resourceType": "connector", "resourceKey": "remote", "mcpUrl": "https://example.test/mcp", "content": `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp"}`}}
	approval, err := h.PrepareToolApproval(t.Context(), "catalog_manage", args, execution)
	if err != nil {
		t.Fatal(err)
	}
	execution.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	result, err := h.Invoke(t.Context(), "catalog_manage", args, execution)
	if err != nil || result.Error != "" || result.Structured["status"] != "applied" || result.Structured["connection"].(map[string]any)["readiness"] != "unknown" || strings.Contains(result.Output, "private-") {
		t.Fatalf("snapshot changed commit result: %+v, %v", result, err)
	}
	if _, err := h.cfg.Paths.ConnectorSources().Load("remote"); err != nil {
		t.Fatal(err)
	}
}

// mcpAuthorizationServer answers an unauthenticated initialize with a Bearer
// challenge, as the MCP authorization specification requires.
func mcpAuthorizationServer(t *testing.T, publishMetadata bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/mcp" && r.Method == http.MethodPost && publishMetadata:
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/resource-metadata"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/mcp":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/resource-metadata":
			json.NewEncoder(w).Encode(map[string]any{"resource": server.URL + "/mcp", "authorization_servers": []string{server.URL}})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "registration_endpoint": server.URL + "/register", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestConnectorCheckReportsAuthorizationDiscoveryFailureWithoutEndpoints(t *testing.T) {
	h := connectorControlFixture(t)
	upstream := mcpAuthorizationServer(t, false)
	change := adminsource.ControlChange{ControlTarget: adminsource.ControlTarget{ResourceType: "connector", ResourceKey: "remote"}, Action: "apply", MCPURL: upstream.URL + "/mcp", Content: `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp"}`}
	plan, err := h.sources.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sources.Apply(t.Context(), change, "caller", plan.Digest); err != nil {
		t.Fatal(err)
	}
	result, err := h.Invoke(t.Context(), "platform_inspect", map[string]any{"action": "checkConnector", "args": map[string]any{"connectorId": "remote"}}, controlExecution())
	authorization, _ := result.Structured["authorization"].(map[string]any)
	if err != nil || result.Structured["status"] != "failed" || authorization["code"] != "resource_metadata_not_found" || result.Structured["nextAction"] != nil || strings.Contains(result.Output, upstream.URL) {
		t.Fatalf("discovery failure: %+v, %v", result, err)
	}
}

func TestConnectorCheckAdmissionAndAuthorizationRecovery(t *testing.T) {
	h := connectorControlFixture(t)
	upstream := mcpAuthorizationServer(t, true)
	change := adminsource.ControlChange{ControlTarget: adminsource.ControlTarget{ResourceType: "connector", ResourceKey: "remote"}, Action: "apply", MCPURL: upstream.URL + "/mcp", Content: `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp"}`}
	plan, err := h.sources.Prepare(change, "caller")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sources.Apply(t.Context(), change, "caller", plan.Digest); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "checkConnector", "args": map[string]any{"connectorId": "remote"}}
	result, err := h.Invoke(t.Context(), "platform_inspect", args, controlExecution())
	if err != nil || result.Error != "" || result.Structured["status"] != "authorization_required" || result.Structured["nextAction"].(map[string]any)["route"] != "/connectors/remote" || result.Structured["authorization"].(map[string]any)["status"] != "passed" {
		t.Fatalf("authorization recovery: %+v, %v", result, err)
	}
	child := controlExecution()
	child.Session.SubTaskID = "child"
	result, _ = h.Invoke(t.Context(), "platform_inspect", args, child)
	if result.Error != "control_request_rejected" {
		t.Fatal("child received deployment connector checking authority")
	}
	args["args"] = map[string]any{"connectorId": "../remote"}
	result, _ = h.Invoke(t.Context(), "platform_inspect", args, controlExecution())
	if result.Structured["field"] != "args.connectorId" {
		t.Fatalf("invalid ID accepted: %+v", result)
	}
	args["args"] = map[string]any{"connectorId": "remote", "toolName": "delete"}
	result, _ = h.Invoke(t.Context(), "platform_inspect", args, controlExecution())
	if result.Error != "control_request_rejected" {
		t.Fatal("business tool arguments accepted by the checker")
	}
}

type pendingConnectionReader struct{}

func (pendingConnectionReader) Connection(context.Context, string) (connectorauth.Connection, error) {
	return connectorauth.Connection{Capabilities: connectorauth.Capabilities{AuthMode: connector.AuthMCP}, Readiness: "preparing", Authentication: connectorauth.Session{Status: "pending", URL: "https://private-authorization-url", Message: "private-auth-message"}}, nil
}

func TestConnectorSnapshotDoesNotExposeLoginURLsOrMessages(t *testing.T) {
	h := connectorControlFixture(t)
	h.connectorConnections = pendingConnectionReader{}
	result := map[string]any{}
	h.addConnectorConnection(t.Context(), "remote", result)
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "private-") || result["nextAction"].(map[string]any)["route"] != "/connectors/remote" {
		t.Fatalf("unsafe authorization snapshot: %s", data)
	}
}

type readyConnectorConnectionReader struct{ mode connector.AuthMode }

func (r readyConnectorConnectionReader) Connection(context.Context, string) (connectorauth.Connection, error) {
	return connectorauth.Connection{ConnectionState: connector.ConnectionState{Configured: true}, Capabilities: connectorauth.Capabilities{AuthMode: r.mode}, Readiness: "ready"}, nil
}

func TestConnectorReadbackRecommendsOnlySupportedChecks(t *testing.T) {
	for _, tc := range []struct {
		name, mode, component string
		check                 bool
	}{
		{"HTTP", "none", `{"type":"streamableHttp","url":"https://example.test/mcp"}`, true},
		{"OAuth", "mcp", `{"type":"streamableHttp","url":"https://example.test/mcp"}`, true},
		{"stdio", "none", `{"type":"stdio","command":"must-never-run"}`, false},
		{"OneID", "oneid-token", `{"type":"streamableHttp","url":"https://example.test/mcp"}`, false},
		{"disabled", "none", `{"type":"streamableHttp","url":"https://example.test/mcp","disabled":true}`, false},
		{"platform-disabled", "none", `{"type":"streamableHttp","url":"https://example.test/mcp","platform":{"enabled":false}}`, false},
		{"enabled-override", "none", `{"type":"streamableHttp","url":"https://example.test/mcp","disabled":false,"platform":{"enabled":false}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := connectorControlFixture(t)
			h.connectorConnections = readyConnectorConnectionReader{connector.AuthMode(tc.mode)}
			dir := filepath.Join(h.cfg.Paths.ConnectorsCenterDir, "remote")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{
				"connector.json": `{"id":"remote","name":"Remote","version":"1.0.0","type":"mcp","authMode":"` + tc.mode + `"}`,
				"mcp.json":       `{"mcpServers":{"main":` + tc.component + `}}`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.cfg.Paths.ConnectorSources().Load("remote"); err != nil {
				t.Fatal(err)
			}
			result := map[string]any{}
			h.addConnectorConnection(t.Context(), "remote", result)
			next, exists := result["nextAction"]
			if exists != tc.check || (exists && next.(map[string]any)["action"] != "checkConnector") {
				t.Fatalf("unsupported check guidance: %#v", result)
			}
		})
	}
}
