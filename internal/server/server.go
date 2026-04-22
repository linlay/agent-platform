package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/models"
	"agent-platform-runner-go/internal/observability"
	"agent-platform-runner-go/internal/skills"
	"agent-platform-runner-go/internal/stream"
	"agent-platform-runner-go/internal/ws"
)

type Dependencies struct {
	Config          config.Config
	Chats           chat.Store
	Memory          memory.Store
	Registry        catalog.Registry
	Models          *models.ModelRegistry
	Runs            contracts.RunManager
	Agent           contracts.AgentEngine
	Tools           contracts.ToolExecutor
	Sandbox         contracts.SandboxClient
	MCP             contracts.McpClient
	Viewport        contracts.ViewportClient
	FrontendTools   *frontendtools.Registry
	CatalogReloader contracts.CatalogReloader
	Notifications   contracts.NotificationSink
	SkillCandidates skills.CandidateStore
}

type Server struct {
	router        *http.ServeMux
	deps          Dependencies
	authVerifier  *JWTVerifier
	ticketService *ResourceTicketService
	wsHandler     *ws.Handler
	proxyMu       sync.RWMutex
	proxyRuns     map[string]*proxyRunRoute
}

type syncQueryContextKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func New(deps Dependencies) (*Server, error) {
	if configurable, ok := deps.Runs.(contracts.RunLifecycleConfigurer); ok {
		configurable.ConfigureRunLifecycle(deps.Config.Run)
	}
	authVerifier := NewJWTVerifier(deps.Config.Auth)
	if deps.Config.Auth.Enabled {
		if err := authVerifier.ValidateConfiguration(); err != nil {
			return nil, fmt.Errorf("validate auth config: %w", err)
		}
		switch authVerifier.Mode() {
		case "local-public-key":
			log.Printf("auth enabled: mode=local-public-key public_key=%s", deps.Config.Auth.LocalPublicKeyFile)
		case "jwks":
			log.Printf("auth enabled: mode=jwks jwks_uri=%s", deps.Config.Auth.JWKSURI)
		}
	} else {
		log.Printf("auth disabled")
	}
	if deps.Notifications == nil {
		deps.Notifications = contracts.NewNoopNotificationSink()
	}
	s := &Server{
		router:        http.NewServeMux(),
		deps:          deps,
		authVerifier:  authVerifier,
		ticketService: NewResourceTicketService(deps.Config.ChatImage),
		proxyRuns:     map[string]*proxyRunRoute{},
	}
	if deps.Config.WebSocket.Enabled {
		if hub, ok := deps.Notifications.(*ws.Hub); ok {
			s.wsHandler = s.newWSHandler(hub)
		}
	}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	if s.handleCORS(w, r) {
		return
	}
	r = s.withPrincipal(r, w)
	if r == nil {
		return
	}
	if s.deps.Config.Logging.Request.Enabled {
		log.Printf("%s %s (arrived)", r.Method, r.URL.RequestURI())
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.router.ServeHTTP(rec, r)
	s.logRequest(r, rec.status, time.Since(startedAt))
}

func (s *Server) WSHandler() *ws.Handler {
	if s == nil {
		return nil
	}
	return s.wsHandler
}

// ExecuteInternalQuery reuses the normal query handling pipeline for
// in-process callers such as the scheduler, while intentionally bypassing the
// outer HTTP auth gate enforced by ServeHTTP.
func (s *Server) ExecuteInternalQuery(ctx context.Context, req api.QueryRequest) (int, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, "", err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)).WithContext(withSyncQueryContext(ctx))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleQuery(rec, httpReq)
	return rec.Code, strings.TrimSpace(rec.Body.String()), nil
}

// ExecuteInternalQueryStream reuses the normal query pipeline for in-process
// callers that need to react to each SSE event as it is emitted (e.g. the
// gateway bridge). onEvent receives the raw JSON payload of each `data:` line
// except the `[DONE]` sentinel. Returning an error from onEvent aborts further
// streaming but does not cancel the underlying run.
func (s *Server) ExecuteInternalQueryStream(
	ctx context.Context,
	req api.QueryRequest,
	onEvent func(eventJSON []byte) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)).WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	rw := newSSEInterceptor(onEvent)
	s.handleQuery(rw, httpReq)
	return rw.err
}

type sseInterceptor struct {
	header  http.Header
	buf     bytes.Buffer
	onEvent func([]byte) error
	err     error
}

func newSSEInterceptor(onEvent func([]byte) error) *sseInterceptor {
	return &sseInterceptor{header: http.Header{}, onEvent: onEvent}
}

func (w *sseInterceptor) Header() http.Header { return w.header }

func (w *sseInterceptor) WriteHeader(int) {}

func (w *sseInterceptor) Write(p []byte) (int, error) {
	n := len(p)
	w.buf.Write(p)
	for {
		idx := bytes.Index(w.buf.Bytes(), []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := make([]byte, idx)
		copy(frame, w.buf.Bytes()[:idx])
		w.buf.Next(idx + 2)
		var payload []byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				chunk := bytes.TrimPrefix(line, []byte("data:"))
				chunk = bytes.TrimPrefix(chunk, []byte(" "))
				if len(payload) > 0 {
					payload = append(payload, '\n')
				}
				payload = append(payload, chunk...)
			}
		}
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(payload), []byte(stream.DoneSentinel)) {
			continue
		}
		if w.err == nil && w.onEvent != nil {
			if err := w.onEvent(payload); err != nil {
				w.err = err
			}
		}
	}
	return n, nil
}

func (w *sseInterceptor) Flush() {}

func withSyncQueryContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, syncQueryContextKey{}, true)
}

func isSyncQueryContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(syncQueryContextKey{}).(bool)
	return value
}

func (s *Server) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	cfg := s.deps.Config.CORS
	if !cfg.Enabled || !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && originAllowed(origin, cfg.AllowedOriginPatterns) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if cfg.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if len(cfg.ExposedHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposedHeaders, ", "))
	}
	if r.Method != http.MethodOptions {
		return false
	}
	if len(cfg.AllowedMethods) > 0 {
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
	}
	if len(cfg.AllowedHeaders) > 0 {
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
	}
	if cfg.MaxAgeSeconds > 0 {
		w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.MaxAgeSeconds))
	}
	w.WriteHeader(http.StatusOK)
	return true
}

func (s *Server) withPrincipal(r *http.Request, w http.ResponseWriter) *http.Request {
	if !s.deps.Config.Auth.Enabled || !strings.HasPrefix(r.URL.Path, "/api/") {
		return r
	}
	if r.Method == http.MethodOptions {
		return r
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/resource" {
		if !s.deps.Config.ChatImage.ResourceTicketEnabled {
			return r
		}
		if strings.TrimSpace(r.URL.Query().Get("t")) != "" {
			return r
		}
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeAuthError(w)
		return nil
	}
	principal, err := s.authVerifier.Verify(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
	if err != nil {
		writeAuthError(w)
		return nil
	}
	return r.WithContext(WithPrincipal(r.Context(), principal))
}

func writeAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

func (s *Server) logRequest(r *http.Request, status int, cost time.Duration) {
	if !s.deps.Config.Logging.Request.Enabled {
		return
	}
	observability.LogRequest(r, status, cost)
	log.Printf("%s %s -> %d (%s)", r.Method, r.URL.RequestURI(), status, cost.Round(time.Millisecond))
}

func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, pattern := range allowed {
		if pattern == "*" || strings.EqualFold(strings.TrimSpace(pattern), origin) {
			return true
		}
	}
	return false
}

func resourceBelongsToChat(fileParam string, chatID string) bool {
	clean := filepath.ToSlash(filepath.Clean(fileParam))
	return clean == chatID || strings.HasPrefix(clean, chatID+"/")
}

func (s *Server) routes() {
	s.router.HandleFunc("/api/agents", s.method(http.MethodGet, s.handleAgents))
	s.router.HandleFunc("/api/agent", s.method(http.MethodGet, s.handleAgent))
	s.router.HandleFunc("/api/teams", s.method(http.MethodGet, s.handleTeams))
	s.router.HandleFunc("/api/skills", s.method(http.MethodGet, s.handleSkills))
	s.router.HandleFunc("/api/skill-candidates", s.method(http.MethodGet, s.handleSkillCandidates))
	s.router.HandleFunc("/api/tools", s.method(http.MethodGet, s.handleTools))
	s.router.HandleFunc("/api/tool", s.method(http.MethodGet, s.handleTool))
	s.router.HandleFunc("/api/chats", s.method(http.MethodGet, s.handleChats))
	s.router.HandleFunc("/api/chat", s.method(http.MethodGet, s.handleChat))
	s.router.HandleFunc("/api/session-search", s.method(http.MethodPost, s.handleSessionSearch))
	s.router.HandleFunc("/api/read", s.method(http.MethodPost, s.handleRead))
	s.router.HandleFunc("/api/query", s.method(http.MethodPost, s.handleQuery))
	s.router.HandleFunc("/api/attach", s.method(http.MethodGet, s.handleAttach))
	s.router.HandleFunc("/api/submit", s.method(http.MethodPost, s.handleSubmit))
	s.router.HandleFunc("/api/steer", s.method(http.MethodPost, s.handleSteer))
	s.router.HandleFunc("/api/interrupt", s.method(http.MethodPost, s.handleInterrupt))
	s.router.HandleFunc("/api/remember", s.method(http.MethodPost, s.handleRemember))
	s.router.HandleFunc("/api/learn", s.method(http.MethodPost, s.handleLearn))
	s.router.HandleFunc("/api/viewport", s.method(http.MethodGet, s.handleViewport))
	s.router.HandleFunc("/api/resource", s.method(http.MethodGet, s.handleResource))
	s.router.HandleFunc("/api/upload", s.method(http.MethodPost, s.handleUpload))
	if s.wsHandler != nil {
		s.router.Handle("/ws", s.wsHandler)
	}
}

func (s *Server) method(expected string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != expected {
			w.Header().Set("Allow", expected)
			writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
			return
		}
		handler(w, r)
	}
}

// enrichToolMetadata fills display fields on tool.snapshot events by looking up
// the tool definition in the registry. LoadChat reconstructs these events from
// JSONL which only has the raw tool name.
func (s *Server) enrichToolMetadata(events []stream.EventData, _ string) {
	lookup := s.toolLookup()
	if lookup == nil {
		return
	}
	for i := range events {
		if events[i].Type != "tool.snapshot" {
			continue
		}
		toolName := events[i].String("toolName")
		if toolName == "" {
			continue
		}
		def, ok := lookup.Tool(toolName)
		if !ok {
			continue
		}
		if events[i].Payload == nil {
			events[i].Payload = map[string]any{}
		}
		if label := def.Label; label != "" {
			events[i].Payload["toolLabel"] = label
		}
	}
}

func (s *Server) toolLookup() contracts.ToolDefinitionLookup {
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		return contracts.NewCompositeToolLookup(tl, s.deps.Registry)
	}
	return s.deps.Registry
}

func (s *Server) toolLookupWithOverrides(overrides map[string]api.ToolDetailResponse) contracts.ToolDefinitionLookup {
	base := s.toolLookup()
	if len(overrides) == 0 {
		return base
	}
	return overrideToolLookup{
		base:      base,
		overrides: cloneToolOverrides(overrides),
	}
}

func (s *Server) lookupInternalTool(toolName string) (api.ToolDetailResponse, bool) {
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		if tool, exists := tl.Tool(toolName); exists {
			return tool, true
		}
	}
	return s.deps.Registry.Tool(toolName)
}

func (s *Server) listTools(kind string, tag string) []api.ToolSummary {
	needleKind := strings.ToLower(strings.TrimSpace(kind))
	needleTag := strings.ToLower(strings.TrimSpace(tag))
	defs := s.deps.Tools.Definitions()
	items := make([]api.ToolSummary, 0, len(defs))
	seen := map[string]struct{}{}
	for _, tool := range defs {
		canonical, ok := canonicalizePublicToolDefinition(tool)
		if !ok {
			continue
		}
		tool = canonical
		metaKind, _ := tool.Meta["kind"].(string)
		if needleKind != "" && strings.ToLower(strings.TrimSpace(metaKind)) != needleKind {
			continue
		}
		if needleTag != "" && !matchesToolTag(tool, needleTag) {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(tool.Name))
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		items = append(items, api.ToolSummary{
			Key:         tool.Key,
			Name:        tool.Name,
			Label:       tool.Label,
			Description: tool.Description,
			Meta:        contracts.CloneMap(tool.Meta),
		})
	}
	return items
}

func (s *Server) lookupTool(toolName string) (api.ToolDetailResponse, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "_sandbox_bash_", "_bash_container_":
		return api.ToolDetailResponse{}, false
	}
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		if tool, exists := tl.Tool(toolName); exists {
			return canonicalizePublicToolDefinition(tool)
		}
	}
	tool, ok := s.deps.Registry.Tool(toolName)
	if !ok {
		return api.ToolDetailResponse{}, false
	}
	return canonicalizePublicToolDefinition(tool)
}

func matchesToolTag(tool api.ToolDetailResponse, needle string) bool {
	fields := []string{
		tool.Key,
		tool.Name,
		tool.Label,
		tool.Description,
		tool.AfterCallHint,
		anyStringValue(tool.Meta["kind"]),
		anyStringValue(tool.Meta["viewportType"]),
		anyStringValue(tool.Meta["viewportKey"]),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

func summaryAgentKey(summary *chat.Summary) string {
	if summary == nil {
		return ""
	}
	return strings.TrimSpace(summary.AgentKey)
}

func applyToolOverride(def api.ToolDetailResponse, overrides map[string]api.ToolDetailResponse) api.ToolDetailResponse {
	if len(overrides) == 0 {
		return def
	}
	override, ok := overrides[strings.ToLower(strings.TrimSpace(def.Name))]
	if !ok {
		override, ok = overrides[strings.ToLower(strings.TrimSpace(def.Key))]
	}
	if !ok {
		return def
	}
	merged := def
	if strings.TrimSpace(override.Key) != "" {
		merged.Key = override.Key
	}
	if strings.TrimSpace(override.Name) != "" {
		merged.Name = override.Name
	}
	if strings.TrimSpace(override.Label) != "" {
		merged.Label = override.Label
	}
	if strings.TrimSpace(override.Description) != "" {
		merged.Description = override.Description
	}
	if strings.TrimSpace(override.AfterCallHint) != "" {
		merged.AfterCallHint = override.AfterCallHint
	}
	if len(override.Parameters) > 0 {
		merged.Parameters = contracts.CloneMap(override.Parameters)
	}
	if len(def.Meta) > 0 {
		merged.Meta = contracts.CloneMap(def.Meta)
	}
	if len(merged.Meta) == 0 {
		merged.Meta = map[string]any{}
	}
	for key, value := range override.Meta {
		merged.Meta[key] = value
	}
	return merged
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func defaultRole(role string) string {
	if strings.TrimSpace(role) == "" {
		return "user"
	}
	return strings.TrimSpace(role)
}

func (s *Server) buildAgentDetailResponse(def catalog.AgentDefinition) api.AgentDetailResponse {
	modelName, meta := s.buildAgentDetailMeta(def)
	return api.AgentDetailResponse{
		Key:         def.Key,
		Name:        def.Name,
		Icon:        def.Icon,
		Description: def.Description,
		Role:        def.Role,
		Wonders:     append([]string(nil), def.Wonders...),
		Model:       modelName,
		Mode:        def.Mode,
		Tools:       effectiveAgentTools(def),
		Skills:      append([]string{}, def.Skills...),
		Controls:    cloneListMaps(def.Controls),
		Meta:        meta,
	}
}

func (s *Server) buildAgentDetailMeta(def catalog.AgentDefinition) (string, map[string]any) {
	modelName := strings.TrimSpace(def.ModelKey)
	meta := map[string]any{}
	if def.ModelKey != "" {
		meta["modelKey"] = def.ModelKey
		meta["modelKeys"] = []string{def.ModelKey}
	}
	if s.deps.Models != nil {
		model, provider, err := s.deps.Models.Get(def.ModelKey)
		if err == nil {
			if strings.TrimSpace(model.ModelID) != "" {
				modelName = strings.TrimSpace(model.ModelID)
			}
			if strings.TrimSpace(model.Key) != "" {
				meta["modelKey"] = model.Key
				meta["modelKeys"] = []string{model.Key}
			}
			if strings.TrimSpace(provider.Key) != "" {
				meta["providerKey"] = provider.Key
			}
			if strings.TrimSpace(model.Protocol) != "" {
				meta["protocol"] = model.Protocol
			}
		}
	}
	if modelName == "" {
		modelName = def.ModelKey
	}
	if len(def.Skills) > 0 {
		meta["perAgentSkills"] = append([]string(nil), def.Skills...)
	}
	if def.Sandbox != nil {
		meta["sandbox"] = normalizedSandboxMeta(def.Sandbox)
	}
	return modelName, meta
}

func normalizedAgentTools(def catalog.AgentDefinition) []string {
	tools := make([]string, 0, len(def.Tools)+1)
	seen := map[string]struct{}{}
	for _, tool := range def.Tools {
		name := strings.TrimSpace(tool)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, name)
	}
	if len(def.Skills) > 0 || hasSandboxConfig(def.Sandbox) {
		if _, ok := seen["_bash_"]; !ok {
			tools = append(tools, "_bash_")
			seen["_bash_"] = struct{}{}
		}
	}
	return tools
}

func effectiveAgentTools(def catalog.AgentDefinition) []string {
	return normalizedAgentTools(def)
}

func hasSandboxConfig(sandbox map[string]any) bool {
	return len(sandbox) > 0
}

func normalizedSandboxMeta(sandbox map[string]any) map[string]any {
	if sandbox == nil {
		return nil
	}
	out := map[string]any{
		"environmentId": stringValue(sandbox["environmentId"]),
		"level":         strings.ToUpper(stringValue(sandbox["level"])),
	}
	// Intentionally do not expose sandbox env values via API metadata.
	if mounts := normalizeSandboxMounts(sandbox["extraMounts"]); len(mounts) > 0 {
		out["extraMounts"] = mounts
	}
	return out
}

func normalizeSandboxMounts(value any) []map[string]any {
	switch mounts := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(mounts))
		for _, mount := range mounts {
			out = append(out, normalizeSandboxMount(mount))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(mounts))
		for _, raw := range mounts {
			mount, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, normalizeSandboxMount(mount))
		}
		return out
	default:
		return nil
	}
}

func normalizeSandboxMount(mount map[string]any) map[string]any {
	return map[string]any{
		"platform":    stringValue(mount["platform"]),
		"source":      nullableStringValue(mount["source"]),
		"destination": nullableStringValue(mount["destination"]),
		"mode":        stringValue(mount["mode"]),
	}
}

func sandboxExtraMounts(value any) []contracts.SandboxExtraMount {
	mounts := normalizeSandboxMounts(value)
	if len(mounts) == 0 {
		return nil
	}
	out := make([]contracts.SandboxExtraMount, 0, len(mounts))
	for _, mount := range mounts {
		out = append(out, contracts.SandboxExtraMount{
			Platform:    stringValue(mount["platform"]),
			Source:      stringValue(mount["source"]),
			Destination: stringValue(mount["destination"]),
			Mode:        stringValue(mount["mode"]),
		})
	}
	return out
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func anyStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func nullableStringValue(value any) any {
	text := stringValue(value)
	if text == "" {
		return nil
	}
	return text
}

func extractSandboxField(sandbox map[string]any, key string) string {
	if sandbox == nil {
		return ""
	}
	v, _ := sandbox[key].(string)
	return strings.TrimSpace(v)
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, contracts.CloneMap(item))
	}
	return out
}

func cloneToolOverrides(src map[string]api.ToolDetailResponse) map[string]api.ToolDetailResponse {
	if src == nil {
		return nil
	}
	out := make(map[string]api.ToolDetailResponse, len(src))
	for key, value := range src {
		out[key] = api.ToolDetailResponse{
			Key:           value.Key,
			Name:          value.Name,
			Label:         value.Label,
			Description:   value.Description,
			AfterCallHint: value.AfterCallHint,
			Parameters:    contracts.CloneMap(value.Parameters),
			Meta:          contracts.CloneMap(value.Meta),
		}
	}
	return out
}

func cloneToolDetailResponse(value api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           value.Key,
		Name:          value.Name,
		Label:         value.Label,
		Description:   value.Description,
		AfterCallHint: value.AfterCallHint,
		Parameters:    contracts.CloneMap(value.Parameters),
		Meta:          contracts.CloneMap(value.Meta),
	}
}

type overrideToolLookup struct {
	base      contracts.ToolDefinitionLookup
	overrides map[string]api.ToolDetailResponse
}

func (o overrideToolLookup) Tool(name string) (api.ToolDetailResponse, bool) {
	if o.base == nil {
		return api.ToolDetailResponse{}, false
	}
	tool, ok := o.base.Tool(name)
	if !ok {
		return api.ToolDetailResponse{}, false
	}
	return applyToolOverride(tool, o.overrides), true
}

func canonicalizePublicToolDefinition(tool api.ToolDetailResponse) (api.ToolDetailResponse, bool) {
	switch strings.ToLower(strings.TrimSpace(tool.Name)) {
	case "_sandbox_bash_", "_bash_container_":
		return api.ToolDetailResponse{}, false
	case "_bash_":
		canonical := cloneToolDetailResponse(tool)
		canonical.Key = "_bash_"
		canonical.Name = "_bash_"
		canonical.Label = "执行命令"
		canonical.Description = "Run a command. Runtime decides whether to execute on the host or inside the sandbox based on the agent's sandboxConfig. Always include a short Chinese description explaining the command purpose."
		return canonical, true
	default:
		return cloneToolDetailResponse(tool), true
	}
}

func newRunID() string {
	return chat.NewRunID()
}

func newChatID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		data[0:4],
		data[4:6],
		data[6:8],
		data[8:10],
		data[10:16],
	)
}

func withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
