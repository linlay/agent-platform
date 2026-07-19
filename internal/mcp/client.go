package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/contracts"
	"agent-platform/internal/observability"

	sdkjsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Client struct {
	registry   *Registry
	httpClient *http.Client
	gate       *AvailabilityGate
	sdk        *sdkmcp.Client

	mu     sync.Mutex
	slots  map[string]*sessionSlot
	closed bool
}

type sessionSlot struct {
	mu      sync.Mutex
	current *managedSession
}

type managedSession struct {
	fingerprint string
	transport   string
	session     *sdkmcp.ClientSession
}

func NewClient(registry *Registry, httpClient *http.Client) *Client {
	return NewClientWithGate(registry, httpClient, nil)
}

func NewClientWithGate(registry *Registry, httpClient *http.Client, gate *AvailabilityGate) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		registry:   registry,
		httpClient: httpClient,
		gate:       gate,
		sdk: sdkmcp.NewClient(&sdkmcp.Implementation{
			Name:    "agent-platform",
			Version: "0.0.1",
		}, &sdkmcp.ClientOptions{Capabilities: &sdkmcp.ClientCapabilities{}}),
		slots: map[string]*sessionSlot{},
	}
}

func (c *Client) Initialize(ctx context.Context, serverKey string) error {
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	_, err := c.ensureSessionWithRetry(ctx, server)
	if err != nil {
		c.markFailure(server.Key)
		return err
	}
	c.markSuccess(server.Key)
	return nil
}

func (c *Client) CallTool(ctx context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error) {
	if c.gate != nil && c.gate.IsBlocked(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", contracts.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	managed, err := c.ensureSessionWithRetry(ctx, server)
	if err != nil {
		c.markFailure(server.Key)
		return nil, err
	}
	callCtx, cancel := operationContext(ctx, server.ReadTimeout)
	defer cancel()
	observability.Log("mcp.request", map[string]any{"serverKey": server.Key, "method": "tools/call"})
	result, err := managed.session.CallTool(callCtx, &sdkmcp.CallToolParams{
		Meta:      sdkmcp.Meta(contracts.CloneMap(meta)),
		Name:      toolName,
		Arguments: defaultArguments(args),
	})
	if err != nil {
		c.invalidate(server.Key, managed)
		c.markFailure(server.Key)
		return nil, fmt.Errorf("%w: %v", contracts.ErrMCPCallFailed, err)
	}
	c.markSuccess(server.Key)
	observability.Log("mcp.response", map[string]any{"serverKey": server.Key, "method": "tools/call"})
	return sdkValue(result)
}

func (c *Client) ListTools(ctx context.Context, serverKey string) ([]ToolDefinition, error) {
	if c.gate != nil && c.gate.IsBlocked(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", contracts.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	managed, err := c.ensureSessionWithRetry(ctx, server)
	if err != nil {
		c.markFailure(server.Key)
		return nil, err
	}
	listCtx, cancel := operationContext(ctx, server.ReadTimeout)
	defer cancel()
	observability.Log("mcp.request", map[string]any{"serverKey": server.Key, "method": "tools/list"})
	definitions := make([]ToolDefinition, 0, 16)
	for tool, listErr := range managed.session.Tools(listCtx, nil) {
		if listErr != nil {
			c.invalidate(server.Key, managed)
			c.markFailure(server.Key)
			return nil, fmt.Errorf("%w: %v", contracts.ErrMCPCallFailed, listErr)
		}
		if tool == nil {
			continue
		}
		definitions = append(definitions, toolDefinitionFromSDK(tool))
	}
	c.markSuccess(server.Key)
	observability.Log("mcp.response", map[string]any{"serverKey": server.Key, "method": "tools/list"})
	return definitions, nil
}

// Reconcile closes sessions for removed or connection-changed registry entries.
// Unchanged sessions remain live and continue to serve concurrent tool calls.
func (c *Client) Reconcile() {
	if c == nil || c.registry == nil {
		return
	}
	active := map[string]string{}
	for _, server := range c.registry.Servers() {
		active[normalizeKey(server.Key)] = serverFingerprint(server)
	}
	c.mu.Lock()
	slots := make(map[string]*sessionSlot, len(c.slots))
	for key, slot := range c.slots {
		slots[key] = slot
		if _, ok := active[key]; !ok {
			delete(c.slots, key)
		}
	}
	c.mu.Unlock()
	for key, slot := range slots {
		slot.mu.Lock()
		current := slot.current
		if current != nil && (active[key] == "" || active[key] != current.fingerprint) {
			slot.current = nil
		}
		slot.mu.Unlock()
		if current != nil && (active[key] == "" || active[key] != current.fingerprint) {
			_ = current.session.Close()
		}
	}
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	slots := c.slots
	c.slots = map[string]*sessionSlot{}
	c.mu.Unlock()
	var errs []error
	for _, slot := range slots {
		slot.mu.Lock()
		current := slot.current
		slot.current = nil
		slot.mu.Unlock()
		if current != nil {
			if err := closeManagedSession(current); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Client) ensureSessionWithRetry(ctx context.Context, server ServerDefinition) (*managedSession, error) {
	retries := server.Retry
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		managed, err := c.ensureSession(ctx, server)
		if err == nil {
			return managed, nil
		}
		lastErr = err
		if attempt == retries || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *Client) ensureSession(ctx context.Context, server ServerDefinition) (*managedSession, error) {
	slot, err := c.slot(server.Key)
	if err != nil {
		return nil, err
	}
	fingerprint := serverFingerprint(server)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.current != nil && slot.current.fingerprint == fingerprint {
		return slot.current, nil
	}
	if slot.current != nil {
		_ = slot.current.session.Close()
		slot.current = nil
	}
	connectCtx, cancel := operationContext(ctx, server.StartupTimeout)
	defer cancel()
	transport, err := c.transport(server)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", contracts.ErrMCPCallFailed, err)
	}
	observability.Log("mcp.request", map[string]any{"serverKey": server.Key, "method": "initialize", "transport": server.Transport})
	session, err := c.sdk.Connect(connectCtx, strictVersionTransport{base: transport}, nil)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported protocol version") {
			return nil, fmt.Errorf("%w: server %s returned an incompatible protocol version (%v); required %s", contracts.ErrMCPCallFailed, server.Key, err, ProtocolVersion)
		}
		return nil, fmt.Errorf("%w: initialize: %v", contracts.ErrMCPCallFailed, err)
	}
	result := session.InitializeResult()
	if result == nil || result.ProtocolVersion != ProtocolVersion {
		negotiated := ""
		if result != nil {
			negotiated = result.ProtocolVersion
		}
		_ = session.Close()
		return nil, fmt.Errorf("%w: server %s negotiated unsupported protocol version %q; required %s", contracts.ErrMCPCallFailed, server.Key, negotiated, ProtocolVersion)
	}
	managed := &managedSession{fingerprint: fingerprint, transport: server.Transport, session: session}
	slot.current = managed
	observability.Log("mcp.response", map[string]any{"serverKey": server.Key, "method": "initialize", "protocolVersion": ProtocolVersion})
	return managed, nil
}

func closeManagedSession(managed *managedSession) error {
	if managed == nil || managed.session == nil {
		return nil
	}
	err := managed.session.Close()
	if managed.transport == TransportStdio {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
	}
	return err
}

// strictVersionTransport closes connections whose initialize result is not a
// protocol version understood by the SDK. The SDK closes normal initialize
// failures itself, but v1.6.1 does not close the connection when a peer returns
// an unknown version. Known legacy versions remain open long enough for the
// caller's exact-version check to report a useful incompatibility error and
// perform the standard session close.
type strictVersionTransport struct {
	base sdkmcp.Transport
}

func (t strictVersionTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	connection, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &strictVersionConnection{
		Connection:    connection,
		initializeIDs: map[string]struct{}{},
	}, nil
}

type strictVersionConnection struct {
	sdkmcp.Connection
	mu            sync.Mutex
	initializeIDs map[string]struct{}
}

func (c *strictVersionConnection) Write(ctx context.Context, message sdkjsonrpc.Message) error {
	if request, ok := message.(*sdkjsonrpc.Request); ok && request.Method == "initialize" {
		c.mu.Lock()
		c.initializeIDs[jsonRPCIDKey(request.ID.Raw())] = struct{}{}
		c.mu.Unlock()
	}
	return c.Connection.Write(ctx, message)
}

func (c *strictVersionConnection) Read(ctx context.Context) (sdkjsonrpc.Message, error) {
	message, err := c.Connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	response, ok := message.(*sdkjsonrpc.Response)
	if !ok {
		return message, nil
	}
	c.mu.Lock()
	key := jsonRPCIDKey(response.ID.Raw())
	_, initialize := c.initializeIDs[key]
	delete(c.initializeIDs, key)
	c.mu.Unlock()
	if !initialize || response.Error != nil {
		return message, nil
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(response.Result, &result) != nil || !sdkKnownProtocolVersion(result.ProtocolVersion) {
		_ = c.Connection.Close()
	}
	return message, nil
}

func jsonRPCIDKey(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func sdkKnownProtocolVersion(version string) bool {
	switch version {
	case ProtocolVersion, "2025-06-18", "2025-03-26", "2024-11-05":
		return true
	default:
		return false
	}
}

func (c *Client) slot(serverKey string) (*sessionSlot, error) {
	key := normalizeKey(serverKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("%w: client is closed", contracts.ErrMCPCallFailed)
	}
	slot := c.slots[key]
	if slot == nil {
		slot = &sessionSlot{}
		c.slots[key] = slot
	}
	return slot, nil
}

func (c *Client) transport(server ServerDefinition) (sdkmcp.Transport, error) {
	switch server.Transport {
	case TransportStdio:
		cmd := exec.Command(server.Command, server.Args...)
		cmd.Dir = server.WorkingDir
		cmd.Env = builtins.EnsureBinInEnv(append(os.Environ(), envPairs(server.Env)...))
		cmd.Stderr = os.Stderr
		return &sdkmcp.CommandTransport{
			Command:           cmd,
			TerminateDuration: time.Second,
		}, nil
	case TransportStreamableHTTP, "":
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   server.ResolvedURL(),
			HTTPClient: c.httpClientForServer(server),
			MaxRetries: server.Retry,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", server.Transport)
	}
}

func (c *Client) invalidate(serverKey string, target *managedSession) {
	c.mu.Lock()
	slot := c.slots[normalizeKey(serverKey)]
	c.mu.Unlock()
	if slot == nil {
		return
	}
	slot.mu.Lock()
	if slot.current == target {
		slot.current = nil
	}
	slot.mu.Unlock()
	if target != nil {
		_ = target.session.Close()
	}
}

func (c *Client) markFailure(serverKey string) {
	if c.gate != nil {
		c.gate.MarkFailure(serverKey)
	}
}

func (c *Client) markSuccess(serverKey string) {
	if c.gate != nil {
		c.gate.MarkSuccess(serverKey)
	}
}

func (c *Client) httpClientForServer(server ServerDefinition) *http.Client {
	base := c.httpClient
	if base == nil {
		base = &http.Client{}
	}
	cloned := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if typed, ok := transport.(*http.Transport); ok && typed != nil {
		typed = typed.Clone()
		if server.ConnectTimeout > 0 {
			typed.DialContext = (&net.Dialer{Timeout: time.Duration(server.ConnectTimeout) * time.Second}).DialContext
		}
		transport = typed
	}
	cloned.Transport = headerRoundTripper{base: transport, headers: server.Headers, authToken: server.AuthToken}
	return &cloned
}

type headerRoundTripper struct {
	base      http.RoundTripper
	headers   map[string]string
	authToken string
}

func (t headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	if token := strings.TrimSpace(t.authToken); token != "" {
		cloned.Header.Set("Authorization", "Bearer "+token)
	}
	return t.base.RoundTrip(cloned)
}

func toolDefinitionFromSDK(tool *sdkmcp.Tool) ToolDefinition {
	meta := contracts.CloneMap(map[string]any(tool.Meta))
	if meta == nil {
		meta = map[string]any{}
	}
	label := strings.TrimSpace(tool.Title)
	if tool.Annotations != nil {
		if label == "" {
			label = strings.TrimSpace(tool.Annotations.Title)
		}
		if tool.Annotations.ReadOnlyHint {
			meta["readOnly"] = true
		}
	}
	return ToolDefinition{
		Key:          tool.Name,
		Name:         tool.Name,
		Label:        label,
		Description:  tool.Description,
		Parameters:   mapValue(tool.InputSchema),
		OutputSchema: mapValue(tool.OutputSchema),
		Meta:         meta,
	}
}

func sdkValue(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func mapValue(value any) map[string]any {
	if value == nil {
		return nil
	}
	converted, err := sdkValue(value)
	if err != nil {
		return nil
	}
	mapped, _ := converted.(map[string]any)
	return mapped
}

func serverFingerprint(server ServerDefinition) string {
	data, _ := json.Marshal(server)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func operationContext(ctx context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if timeoutSeconds <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
}

func defaultArguments(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

func envPairs(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "=\x00") {
			continue
		}
		out = append(out, key+"="+values[key])
	}
	return out
}
