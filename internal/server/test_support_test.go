package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/llm"
	"agent-platform/internal/memory"
	"agent-platform/internal/models"
	"agent-platform/internal/reload"
	"agent-platform/internal/stream"
	"agent-platform/internal/tools"
)

var disallowedPersistedEventTypes = []string{
	"reasoning.start",
	"reasoning.delta",
	"reasoning.end",
	"content.start",
	"content.delta",
	"content.end",
	"tool.start",
	"tool.args",
	"tool.end",
	"action.start",
	"action.args",
	"action.end",
}

func newServerFromFixture(t *testing.T, fixture testFixture) *Server {
	t.Helper()
	server, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server
}

type testFixture struct {
	server          *Server
	cfg             config.Config
	chats           chat.Store
	memories        memory.Store
	registry        catalog.Registry
	modelRegistry   *models.ModelRegistry
	runs            contracts.RunManager
	agent           contracts.AgentEngine
	tools           contracts.ToolExecutor
	frontend        *frontendtools.Registry
	sandbox         contracts.SandboxClient
	mcp             contracts.McpClient
	viewport        contracts.ViewportClient
	catalogReloader contracts.CatalogReloader
}

type testFixtureOptions struct {
	sandbox       contracts.SandboxClient
	mcp           contracts.McpClient
	mcpTools      stubMCPToolCatalog
	notifications contracts.NotificationSink
	configure     func(*config.Config)
	setupRuntime  func(root string, cfg *config.Config)
}

func newTestFixture(t *testing.T) testFixture {
	return newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runtime "}}]}`,
			`{"choices":[{"delta":{"content":"test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
}

func newMemoryEnabledTestFixture(t *testing.T) testFixture {
	return newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runtime "}}]}`,
			`{"choices":[{"delta":{"content":"test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.Memory.Enabled = true
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatalf("read agent config: %v", err)
			}
			content := strings.TrimSpace(string(data)) + "\n" +
				"memoryConfig:\n" +
				"  enabled: true\n" +
				"  autoRemember:\n" +
				"    enabled: true\n"
			if err := os.WriteFile(agentPath, []byte(content), 0o644); err != nil {
				t.Fatalf("write agent config: %v", err)
			}
		},
	})
}

func newTestFixtureWithModelHandler(t *testing.T, modelHandler http.HandlerFunc) testFixture {
	return newTestFixtureWithModelHandlerAndOptions(t, modelHandler, testFixtureOptions{})
}

func newTestFixtureWithModelHandlerAndOptions(t *testing.T, modelHandler http.HandlerFunc, options testFixtureOptions) testFixture {
	t.Helper()
	root := t.TempDir()
	providerServer := newLoopbackServer(t, modelHandler)
	t.Cleanup(providerServer.Close)
	containerHubServer := newLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/environments/") && strings.HasSuffix(r.URL.Path, "/agent-prompt") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"environmentName":"shell","hasPrompt":true,"prompt":"Mock sandbox prompt"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(containerHubServer.Close)

	registriesDir := filepath.Join(root, "registries")
	agentsDir := filepath.Join(root, "agents")
	teamsDir := filepath.Join(root, "teams")
	skillsDir := filepath.Join(root, "skills-market")
	providersDir := filepath.Join(registriesDir, "providers")
	modelsDir := filepath.Join(registriesDir, "models")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agentsDir, "mock-agent"), 0o755); err != nil {
		t.Fatalf("mkdir agents dir: %v", err)
	}
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatalf("mkdir teams dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(skillsDir, "mock-skill"), 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(strings.Join([]string{
		"key: mock",
		"baseUrl: " + providerServer.URL,
		"apiKey: test-key",
		"defaultModel: mock-model",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write provider config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(strings.Join([]string{
		"key: mock-model",
		"name: Mock Model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
		"isFunction: true",
		"isReasoner: false",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "mock-agent", "agent.yml"), []byte(strings.Join([]string{
		"key: mock-agent",
		"name: Mock Agent",
		"role: 测试代理",
		"description: test agent",
		"greetings:",
		"  - 我可以帮你演示平台工具、审批交互和运行时上下文。",
		"  - 你可以把我当作一个用于验证 agent-platform 能力的测试智能体。",
		"wonders:",
		"  - 帮我演示提问式确认",
		"  - |",
		"    帮我演示 Bash HITL 审批确认",
		"    并说明用户接下来会看到什么",
		"modelConfig:",
		"  modelKey: mock-model",
		"toolConfig:",
		"  tools:",
		"    - datetime",
		"    - ask_user_question",
		"skillConfig:",
		"  skills:",
		"    - mock-skill",
		"controls:",
		"  - key: tone",
		"    type: select",
		"    label: 输出语气",
		"    defaultValue: concise",
		"    options:",
		"      - value: concise",
		"        label: 简洁",
		"runtimeConfig:",
		"  environmentId: shell",
		"  level: RUN",
		"  env:",
		"    HTTP_PROXY: http://agent-proxy",
		"    TZ: Asia/Shanghai",
		"  sandboxMounts:",
		"    - platform: skills-market",
		"      destination: /skills",
		"      mode: ro",
		"mode: REACT",
		"budget:",
		"  tool:",
		"    timeout: 210",
		"  hitl:",
		"    timeout: 210",
		"react:",
		"  maxSteps: 6",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write agent config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamsDir, "default.demo.yml"), []byte(strings.Join([]string{
		"name: Default Team",
		"defaultAgentKey: mock-agent",
		"agentKeys:",
		"  - mock-agent",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write team config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "mock-skill", "SKILL.md"), []byte("# Mock Skill\n\nSkill description"), 0o644); err != nil {
		t.Fatalf("write skill config: %v", err)
	}

	cfg := config.Config{
		Server: config.ServerConfig{
			Port: "18080",
		},
		Paths: config.PathsConfig{
			RegistriesDir:   registriesDir,
			AgentsDir:       agentsDir,
			TeamsDir:        teamsDir,
			SkillsMarketDir: skillsDir,
			ChatsDir:        filepath.Join(root, "custom-chats"),
			MemoryDir:       filepath.Join(root, "custom-memory"),
		},
		Auth: config.AuthConfig{
			Enabled: false,
		},
		Defaults: config.DefaultsConfig{
			React: config.ReactDefaultsConfig{MaxSteps: 6},
		},
		Logging: config.LoggingConfig{
			Request: config.ToggleConfig{Enabled: true},
		},
		Skills: config.SkillCatalogConfig{
			CatalogConfig:  config.CatalogConfig{ExternalDir: skillsDir},
			MaxPromptChars: 8000,
		},
		Bash: config.BashConfig{
			WorkingDirectory: root,
			AllowedCommands:  []string{"pwd", "echo", "ls", "cat"},
			ShellExecutable:  "bash",
			MaxCommandChars:  16000,
		},
		ContainerHub: config.ContainerHubConfig{
			Enabled:        true,
			BaseURL:        containerHubServer.URL,
			RequestTimeout: 1,
			ResolvedEngine: "local",
		},
	}
	if options.configure != nil {
		options.configure(&cfg)
	}
	if options.setupRuntime != nil {
		options.setupRuntime(root, &cfg)
	}

	chats, err := chat.NewFileStore(cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	t.Cleanup(func() {
		_ = chats.Close()
	})
	memories, err := memory.NewFileStore(cfg.Paths.MemoryDir)
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	modelRegistry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	sandboxClient := options.sandbox
	if sandboxClient == nil {
		sandboxClient = contracts.NewNoopSandboxClient()
	}
	backendTools, err := tools.NewRuntimeToolExecutor(cfg, sandboxClient, chats, memories, nil)
	if err != nil {
		t.Fatalf("new runtime tool executor: %v", err)
	}
	mcp := options.mcp
	if mcp == nil {
		mcp = contracts.NewNoopMcpClient()
	}
	frontendRegistry := frontendtools.NewDefaultRegistry()
	var mcpTools interface {
		Definitions() []api.ToolDetailResponse
		Tool(name string) (api.ToolDetailResponse, bool)
	}
	if len(options.mcpTools.defs) > 0 {
		mcpTools = options.mcpTools
	}
	toolExecutor := tools.NewToolRouter(backendTools, mcp, mcpTools, llm.NewFrontendSubmitCoordinator(frontendRegistry), contracts.NewNoopActionInvoker())
	registry, err := catalog.NewFileRegistry(cfg, toolExecutor.Definitions())
	if err != nil {
		t.Fatalf("new file registry: %v", err)
	}
	notifications := options.notifications
	if notifications == nil {
		notifications = contracts.NewNoopNotificationSink()
	}
	reloader := reload.NewRuntimeCatalogReloader(registry, modelRegistry, nil, nil, "", notifications)

	runs := contracts.NewInMemoryRunManager()
	sandbox := sandboxClient
	agentEngine := llm.NewLLMAgentEngine(cfg, modelRegistry, toolExecutor, frontendRegistry, sandbox)
	viewport := contracts.NewNoopViewportClient()
	server, err := New(Dependencies{
		Config:          cfg,
		Chats:           chats,
		Memory:          memories,
		Registry:        registry,
		Models:          modelRegistry,
		Runs:            runs,
		Agent:           agentEngine,
		Tools:           toolExecutor,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: frontendRegistry},
		SystemInits:     llm.SystemInitProfileBuilder{Models: modelRegistry},
		Sandbox:         sandbox,
		MCP:             mcp,
		FrontendTools:   frontendRegistry,
		Viewport:        viewport,
		CatalogReloader: reloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	return testFixture{
		server:          server,
		cfg:             cfg,
		chats:           chats,
		memories:        memories,
		registry:        registry,
		modelRegistry:   modelRegistry,
		runs:            runs,
		agent:           agentEngine,
		tools:           toolExecutor,
		frontend:        frontendRegistry,
		sandbox:         sandbox,
		mcp:             mcp,
		viewport:        viewport,
		catalogReloader: reloader,
	}
}

type loopbackServer struct {
	URL    string
	server *http.Server
	ln     net.Listener
}

func (s *loopbackServer) Close() {
	if s == nil {
		return
	}
	if s.server != nil {
		_ = s.server.Close()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func newLoopbackServer(t *testing.T, handler http.Handler) *loopbackServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback server: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	result := &loopbackServer{
		URL:    "http://" + listener.Addr().String(),
		server: server,
		ln:     listener,
	}
	t.Cleanup(result.Close)
	return result
}

func writeProviderSSE(t *testing.T, w http.ResponseWriter, frames ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatalf("expected flusher")
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
			t.Fatalf("write sse frame: %v", err)
		}
		flusher.Flush()
	}
}

func providerToolCallFrame(t *testing.T, toolID string, toolName string, args map[string]any) string {
	t.Helper()
	return providerToolCallsFrame(t, []providerToolCallSpec{{ID: toolID, Name: toolName, Args: args}})
}

type providerToolCallSpec struct {
	ID   string
	Name string
	Args map[string]any
}

func providerToolCallsFrame(t *testing.T, calls []providerToolCallSpec) string {
	t.Helper()
	toolCalls := make([]any, 0, len(calls))
	for index, call := range calls {
		argsJSON, err := json.Marshal(call.Args)
		if err != nil {
			t.Fatalf("marshal tool args: %v", err)
		}
		toolCalls = append(toolCalls, map[string]any{
			"index": index,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": string(argsJSON),
			},
		})
	}
	frame, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"tool_calls": toolCalls,
				},
				"finish_reason": "tool_calls",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal provider tool call frame: %v", err)
	}
	return string(frame)
}

func assertPersistedEventTypes(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	seen := make(map[string]int)
	for _, event := range events {
		eventType := event.Type
		seen[eventType]++
	}
	for _, eventType := range want {
		if seen[eventType] == 0 {
			t.Fatalf("expected persisted event type %q, got %#v", eventType, events)
		}
	}
	for _, eventType := range disallowedPersistedEventTypes {
		if seen[eventType] > 0 {
			t.Fatalf("did not expect persisted event type %q, got %#v", eventType, events)
		}
	}
}

func assertPersistedEventsStartWith(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	if len(events) < len(want) {
		t.Fatalf("expected at least %d persisted events, got %#v", len(want), events)
	}
	for index, eventType := range want {
		if events[index].Type != eventType {
			t.Fatalf("persisted event %d: expected %s, got %#v", index, eventType, events[index])
		}
	}
}

func writeTestJWTKeyPair(t *testing.T, dir string) (*rsa.PrivateKey, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	path := filepath.Join(dir, "test-public-key.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(path, block, 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	return privateKey, path
}

func mustSignRS256JWT(t *testing.T, privateKey *rsa.PrivateKey, payload map[string]any) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func decodeSSEMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	lines := strings.Split(body, "\n")
	messages := make([]map[string]any, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		var msg map[string]any
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			t.Fatalf("decode sse message %q: %v", payload, err)
		}
		messages = append(messages, msg)
	}
	return messages
}

func findToolResultPayload(t *testing.T, body string, toolID string) map[string]any {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if message["type"] == "tool.result" && message["toolId"] == toolID {
			return message
		}
	}
	t.Fatalf("expected tool.result for %s in body %s", toolID, body)
	return nil
}

func assertSSEEventDuration(t *testing.T, body string, eventType string) {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if message["type"] != eventType {
			continue
		}
		duration, ok := message["durationMs"].(float64)
		if !ok || duration < 0 {
			t.Fatalf("expected non-negative durationMs on %s, got %#v", eventType, message)
		}
		return
	}
	t.Fatalf("expected %s in body %s", eventType, body)
}

func findToolMessageContent(t *testing.T, messages []map[string]any, toolName string) string {
	t.Helper()
	for _, message := range messages {
		if message["role"] != "tool" || message["name"] != toolName {
			continue
		}
		content, _ := message["content"].(string)
		if content != "" {
			return content
		}
	}
	t.Fatalf("expected tool message for %s in %#v", toolName, messages)
	return ""
}

func decodeSSEPayloadStrings(body string) []string {
	lines := strings.Split(body, "\n")
	payloads := make([]string, 0)
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
	}
	return payloads
}

func decodeSSELine(t *testing.T, line string) map[string]any {
	t.Helper()
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	var message map[string]any
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("decode sse line %q: %v", line, err)
	}
	return message
}

func normalizeProviderMessages(value any) []map[string]any {
	items, _ := value.([]any)
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		message, _ := item.(map[string]any)
		messages = append(messages, message)
	}
	return messages
}

func assertSSEMessagesHaveSeqAndTimestamp(t *testing.T, body string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) == 0 {
		t.Fatalf("expected sse messages, got body %s", body)
	}
	prevSeq := 0.0
	for _, msg := range messages {
		seq, ok := msg["seq"].(float64)
		if !ok || seq <= prevSeq {
			t.Fatalf("expected ascending seq, got %#v", messages)
		}
		prevSeq = seq
		if _, ok := msg["type"].(string); !ok {
			t.Fatalf("expected type field, got %#v", msg)
		}
		if ts, ok := msg["timestamp"].(float64); !ok || ts <= 0 {
			t.Fatalf("expected positive timestamp, got %#v", msg)
		}
	}
}

func assertSSEEventOrder(t *testing.T, body string, want ...string) {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	if len(messages) < len(want) {
		t.Fatalf("expected at least %d messages, got %#v", len(want), messages)
	}
	for idx, eventType := range want {
		if messages[idx]["type"] != eventType {
			t.Fatalf("event %d: expected %s, got %#v", idx, eventType, messages[idx])
		}
	}
}

func findSSEMessageByType(t *testing.T, messages []map[string]any, eventType string) map[string]any {
	t.Helper()
	for _, message := range messages {
		if message["type"] == eventType {
			return message
		}
	}
	t.Fatalf("expected sse message type %s, got %#v", eventType, messages)
	return nil
}

func assertSSEPayloadOrder(t *testing.T, body string, eventType string, parts []string) {
	t.Helper()
	for _, payload := range decodeSSEPayloadStrings(body) {
		if !strings.Contains(payload, `"type":"`+eventType+`"`) {
			continue
		}
		assertOrderedSubstrings(t, payload, parts)
		return
	}
	t.Fatalf("expected sse event type %s in body %s", eventType, body)
}

func assertBodyContainsOrderedEvent(t *testing.T, body string, marker string, parts []string) {
	t.Helper()
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("expected marker %q in body %s", marker, body)
	}
	start := strings.LastIndex(body[:index], "{")
	end := strings.Index(body[index:], "}")
	if start < 0 || end < 0 {
		t.Fatalf("expected json object around marker %q in body %s", marker, body)
	}
	assertOrderedSubstrings(t, body[start:index+end+1], parts)
}

func assertOrderedSubstrings(t *testing.T, body string, parts []string) {
	t.Helper()
	prev := -1
	for _, part := range parts {
		idx := strings.Index(body, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, body)
		}
		if idx <= prev {
			t.Fatalf("expected ordered substrings %v in %s", parts, body)
		}
		prev = idx
	}
}

func assertUUIDLike(t *testing.T, value string) {
	t.Helper()
	parts := strings.Split(value, "-")
	if len(parts) != 5 {
		t.Fatalf("expected uuid-like value, got %q", value)
	}
	lengths := []int{8, 4, 4, 4, 12}
	for idx, part := range parts {
		if len(part) != lengths[idx] {
			t.Fatalf("expected uuid-like value, got %q", value)
		}
	}
}

func decodeEventTypesFromSSE(t *testing.T, body string) []string {
	t.Helper()
	messages := decodeSSEMessages(t, body)
	types := make([]string, 0, len(messages))
	for _, message := range messages {
		eventType, _ := message["type"].(string)
		if eventType != "" {
			types = append(types, eventType)
		}
	}
	return types
}

func providerRequestToolNames(value any) []string {
	items, _ := value.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		spec, _ := item.(map[string]any)
		function, _ := spec["function"].(map[string]any)
		name := stringValue(function["name"])
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func assertEventTypesInclude(t *testing.T, events []stream.EventData, want ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	assertStringSliceContains(t, got, want...)
}

func assertEventTypesExclude(t *testing.T, events []stream.EventData, blocked ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Type)
	}
	assertStringSliceExcludes(t, got, blocked...)
}

func assertStringSliceContains(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, target := range want {
		found := false
		for _, item := range got {
			if item == target {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in %#v", target, got)
		}
	}
}

func assertStringSliceExcludes(t *testing.T, got []string, blocked ...string) {
	t.Helper()
	for _, target := range blocked {
		for _, item := range got {
			if item == target {
				t.Fatalf("did not expect %q in %#v", target, got)
			}
		}
	}
}
