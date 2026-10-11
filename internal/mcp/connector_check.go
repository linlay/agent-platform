package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/httpclient"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type ConnectorCheckStage struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type ConnectorToolSummary struct {
	Name string `json:"name"`
}

type ConnectorToolDiscovery struct {
	ConnectorCheckStage
	Count     int                    `json:"count"`
	Tools     []ConnectorToolSummary `json:"tools"`
	Truncated bool                   `json:"truncated"`
}

type ConnectorCheckResult struct {
	ConnectorID     string                 `json:"connectorId"`
	Component       string                 `json:"component"`
	AuthMode        connector.AuthMode     `json:"authMode,omitempty"`
	Status          string                 `json:"status"`
	Package         ConnectorCheckStage    `json:"package"`
	Initialize      ConnectorCheckStage    `json:"initialize"`
	ToolDiscovery   ConnectorToolDiscovery `json:"toolDiscovery"`
	ProtocolVersion string                 `json:"protocolVersion,omitempty"`
}

// ConnectorChecker probes saved HTTP components without mounting or registering
// tools. It never starts stdio/CLI processes, logs in, or calls business tools.
type ConnectorChecker struct {
	sources    connector.Sources
	httpClient *http.Client
}

func NewConnectorChecker(sources connector.Sources) *ConnectorChecker {
	return &ConnectorChecker{sources: sources, httpClient: httpclient.NewClient(0)}
}

// SupportsConnectorCheck reads only the declared transport, authentication and
// enabled flags. It does not read credentials, validate the package or connect.
func SupportsConnectorCheck(pkg connector.Package, component string) bool {
	if pkg.AuthMode != connector.AuthNoAuth && pkg.AuthMode != connector.AuthMCP && pkg.AuthMode != connector.AuthOAuth && pkg.AuthMode != connector.AuthToken {
		return false
	}
	definition := pkg.MCP[component]
	if definition == nil || (definition["type"] != "http" && definition["type"] != "streamableHttp") {
		return false
	}
	enabled := true
	if platform, ok := definition["platform"].(map[string]any); ok {
		if source, _ := platform["authSource"].(string); strings.TrimSpace(source) != "" {
			return false
		}
		if value, exists := platform["enabled"]; exists {
			var valid bool
			enabled, valid = value.(bool)
			if !valid {
				return false
			}
		}
	}
	// The component flag overrides platform.enabled, as in connectorServer.
	if value, exists := definition["disabled"]; exists {
		disabled, valid := value.(bool)
		if !valid {
			return false
		}
		enabled = !disabled
	}
	return enabled
}

func (c *ConnectorChecker) CheckConnector(ctx context.Context, id, component string) (ConnectorCheckResult, error) {
	if component == "" {
		component = "main"
	}
	r := ConnectorCheckResult{
		ConnectorID: id, Component: component, Status: "failed",
		Package: ConnectorCheckStage{Status: "not_run"}, Initialize: ConnectorCheckStage{Status: "not_run"},
		ToolDiscovery: ConnectorToolDiscovery{ConnectorCheckStage: ConnectorCheckStage{Status: "not_run"}, Tools: []ConnectorToolSummary{}},
	}
	pkg, err := c.sources.Load(id)
	if err != nil {
		r.Package = ConnectorCheckStage{Status: "failed", Code: "package_unavailable", Message: "Read the connector catalog and resolve its source diagnostics."}
		return r, nil
	}
	r.AuthMode = pkg.AuthMode
	if err := ValidateConnectorPackage(pkg); err != nil {
		r.Package = ConnectorCheckStage{Status: "failed", Code: "invalid_package", Message: "The connector package failed local MCP validation."}
		return r, nil
	}
	if pkg.MCP[component] == nil {
		r.Package = ConnectorCheckStage{Status: "failed", Code: "component_not_found", Message: "Select an existing MCP component from catalog_query."}
		return r, nil
	}
	server, err := connectorServer(pkg, component)
	if err != nil {
		r.Package = ConnectorCheckStage{Status: "failed", Code: "invalid_component", Message: "The selected MCP component failed local validation."}
		return r, nil
	}
	if !server.Enabled() {
		r.Package.Status = "passed"
		r.Initialize.Code, r.Initialize.Message = "component_disabled", "Enable the selected MCP component before checking it."
		return r, nil
	}
	if !SupportsConnectorCheck(pkg, component) {
		r.Package = ConnectorCheckStage{Status: "failed", Code: "unsupported_component", Message: "This check supports HTTP MCP with no_auth, mcp, oauth or token authentication."}
		return r, nil
	}
	r.Package.Status = "passed"
	if pkg.AuthMode != connector.AuthNoAuth {
		state, err := pkg.ReadConnection()
		if err != nil {
			r.Initialize.Code, r.Initialize.Message = "connection_state_unavailable", "Connector configuration state is unavailable."
			return r, nil
		}
		if !state.Configured || server.SetupError != "" {
			r.Status = "authorization_required"
			r.Initialize.Code = "authorization_required"
			r.Initialize.Message = "Complete connector authorization before checking the remote MCP protocol."
			return r, nil
		}
	}

	// Bound remote initialization, SDK retries and discovery. The
	// isolated registry never enters the Agent registry or background sync gate.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	server.Retry = 0
	server.IsolatedEnvironment = true
	base := *c.httpClient
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	probeTransport := &connectorCheckTransport{base: transport}
	base.Transport = probeTransport
	probe := NewClientWithGate(&Registry{servers: map[string]ServerDefinition{server.Key: server}, version: 1}, &base, nil)
	defer probe.Close()
	managed, err := probe.ensureSessionWithRetry(ctx, server)
	if err != nil {
		r.Initialize = connectorProbeFailure(ctx, err)
		setConnectorProbeAuth(&r, probeTransport)
		return r, nil
	}
	r.ProtocolVersion = managed.session.InitializeResult().ProtocolVersion
	managed.release()
	r.Initialize.Status = "passed"
	// A recovered 401 during initialization must not taint a later failure.
	probeTransport.authStatus.Store(0)
	tools, err := probe.ListTools(ctx, server.Key)
	if err != nil {
		r.ToolDiscovery.ConnectorCheckStage = connectorProbeFailure(ctx, err)
		setConnectorProbeAuth(&r, probeTransport)
		return r, nil
	}
	r.ToolDiscovery.Status = "passed"
	r.ToolDiscovery.Count = len(tools)
	for _, tool := range tools {
		if len(r.ToolDiscovery.Tools) == 100 {
			r.ToolDiscovery.Truncated = true
			break
		}
		name := tool.Name
		if len([]rune(name)) > 256 {
			name = string([]rune(name)[:256])
			r.ToolDiscovery.Truncated = true
		}
		r.ToolDiscovery.Tools = append(r.ToolDiscovery.Tools, ConnectorToolSummary{Name: name})
	}
	r.Status = "passed"
	return r, nil
}

func connectorProbeFailure(ctx context.Context, err error) ConnectorCheckStage {
	stage := ConnectorCheckStage{Status: "failed", Code: "mcp_check_failed", Message: "The remote MCP check failed; no business tools were called."}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || isConnectorProbeTimeout(err):
		stage.Code, stage.Message = "timeout", "The MCP check exceeded its time limit."
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		stage.Code, stage.Message = "canceled", "The MCP check was canceled."
	case strings.Contains(err.Error(), "unsupported protocol version"):
		stage.Code, stage.Message = "unsupported_protocol", "The remote MCP protocol version is unsupported."
	}
	// Never return SDK/HTTP error text: it may contain endpoints or credentials.
	return stage
}

func isConnectorProbeTimeout(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func setConnectorProbeAuth(r *ConnectorCheckResult, transport *connectorCheckTransport) {
	stage := &r.Initialize
	if r.Initialize.Status == "passed" {
		stage = &r.ToolDiscovery.ConnectorCheckStage
	}
	// A previous HTTP rejection is not the cause of a later timeout, cancellation
	// or protocol failure. Only refine an otherwise unclassified remote failure.
	status := transport.authStatus.Load()
	if status == 0 || stage.Code != "mcp_check_failed" {
		return
	}
	if status == http.StatusForbidden {
		stage.Code, stage.Message = "permission_denied", "The endpoint denied access; review its access policy and connector permissions."
		return
	}
	if r.AuthMode == connector.AuthNoAuth {
		stage.Code, stage.Message = "authentication_mismatch", "The endpoint rejected unauthenticated access; review the connector authentication mode."
		return
	}
	r.Status = "authorization_required"
	stage.Code, stage.Message = "authorization_required", "The endpoint rejected authorization; complete or renew connector authorization."
}

type connectorCheckTransport struct {
	base       http.RoundTripper
	authStatus atomic.Int32
}

func (t *connectorCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	// GET is the independent SSE stream and DELETE closes the session. Neither
	// can classify an initialize/tools-list POST failure. Record the latest POST
	// outcome, including network errors and successful recovery from a rejection.
	if req.Method == http.MethodPost {
		status := int32(0)
		if err == nil && response != nil {
			switch response.StatusCode {
			case http.StatusUnauthorized:
				status = http.StatusUnauthorized
			case http.StatusForbidden:
				status = http.StatusForbidden
				challenges, _ := oauthex.ParseWWWAuthenticate(response.Header.Values("WWW-Authenticate"))
				for _, challenge := range challenges {
					if strings.EqualFold(challenge.Scheme, "Bearer") && challenge.Params["error"] == "insufficient_scope" {
						status = connectorProbeInsufficientScope
					}
				}
			}
		}
		t.authStatus.Store(status)
	}
	return response, err
}

const connectorProbeInsufficientScope = 1003
