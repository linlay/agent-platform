package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/engine"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/observability"
	"agent-platform-runner-go/internal/stream"
)

type Dependencies struct {
	Config          config.Config
	Chats           chat.Store
	Memory          memory.Store
	Registry        catalog.Registry
	Models          *engine.ModelRegistry
	Runs            engine.RunManager
	Agent           engine.AgentEngine
	Tools           engine.ToolExecutor
	Sandbox         engine.SandboxClient
	MCP             engine.McpClient
	Viewport        engine.ViewportClient
	CatalogReloader engine.CatalogReloader
}

type Server struct {
	router        *http.ServeMux
	deps          Dependencies
	authVerifier  *JWTVerifier
	ticketService *ResourceTicketService
}

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

func New(deps Dependencies) (*Server, error) {
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
	s := &Server{
		router:        http.NewServeMux(),
		deps:          deps,
		authVerifier:  authVerifier,
		ticketService: NewResourceTicketService(deps.Config.ChatImage),
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
	s.router.HandleFunc("/api/tools", s.method(http.MethodGet, s.handleTools))
	s.router.HandleFunc("/api/tool", s.method(http.MethodGet, s.handleTool))
	s.router.HandleFunc("/api/chats", s.method(http.MethodGet, s.handleChats))
	s.router.HandleFunc("/api/chat", s.method(http.MethodGet, s.handleChat))
	s.router.HandleFunc("/api/read", s.method(http.MethodPost, s.handleRead))
	s.router.HandleFunc("/api/query", s.method(http.MethodPost, s.handleQuery))
	s.router.HandleFunc("/api/submit", s.method(http.MethodPost, s.handleSubmit))
	s.router.HandleFunc("/api/steer", s.method(http.MethodPost, s.handleSteer))
	s.router.HandleFunc("/api/interrupt", s.method(http.MethodPost, s.handleInterrupt))
	s.router.HandleFunc("/api/remember", s.method(http.MethodPost, s.handleRemember))
	s.router.HandleFunc("/api/learn", s.method(http.MethodPost, s.handleLearn))
	s.router.HandleFunc("/api/viewport", s.method(http.MethodGet, s.handleViewport))
	s.router.HandleFunc("/api/resource", s.method(http.MethodGet, s.handleResource))
	s.router.HandleFunc("/api/upload", s.method(http.MethodPost, s.handleUpload))
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

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Agents(r.URL.Query().Get("tag"))))
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "agent not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.buildAgentDetailResponse(def)))
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Teams()))
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Skills(r.URL.Query().Get("tag"))))
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Success(s.deps.Registry.Tools(r.URL.Query().Get("kind"), r.URL.Query().Get("tag"))))
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	toolName := r.URL.Query().Get("toolName")
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "toolName is required"))
		return
	}
	tool, ok := s.deps.Registry.Tool(toolName)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool not found"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(tool))
}

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	items, err := s.deps.Chats.ListChats(r.URL.Query().Get("lastRunId"), r.URL.Query().Get("agentKey"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	response := make([]api.ChatSummaryResponse, 0, len(items))
	for _, item := range items {
		response = append(response, api.ChatSummaryResponse(item))
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chatId")
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	detail, err := s.deps.Chats.LoadChat(chatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	includeRaw := strings.EqualFold(r.URL.Query().Get("includeRawMessages"), "true")
	response := api.ChatDetailResponse{
		ChatID:     detail.ChatID,
		ChatName:   detail.ChatName,
		Events:     detail.Events,
		References: nil,
	}
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		response.ChatImageToken = s.ticketService.Issue(principal.Subject, detail.ChatID)
	}
	if includeRaw {
		response.RawMessages = detail.RawMessages
	}
	if detail.Plan != nil {
		response.Plan = detail.Plan
	}
	if detail.Artifact != nil {
		response.Artifact = detail.Artifact
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req api.MarkChatReadRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ChatID) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	summary, err := s.deps.Chats.MarkRead(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	readAt := int64(0)
	if summary.ReadAt != nil {
		readAt = *summary.ReadAt
	}
	writeJSON(w, http.StatusOK, api.Success(api.MarkChatReadResponse{
		ChatID:     summary.ChatID,
		ReadStatus: summary.ReadStatus,
		ReadAt:     readAt,
	}))
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req api.QueryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid request body"))
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "message is required"))
		return
	}

	now := time.Now().UnixMilli()
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		runID = newRunID()
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = runID
	}
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		chatID = newChatID()
	}
	existingSummary, _ := s.deps.Chats.Summary(chatID)
	teamID := strings.TrimSpace(req.TeamID)
	if teamID == "" && existingSummary != nil {
		teamID = existingSummary.TeamID
	}
	agentKey := strings.TrimSpace(req.AgentKey)
	if agentKey == "" && existingSummary != nil {
		agentKey = existingSummary.AgentKey
	}
	if agentKey == "" && teamID != "" {
		if team, ok := s.deps.Registry.TeamDefinition(teamID); ok && team.DefaultAgentKey != "" {
			agentKey = team.DefaultAgentKey
		}
	}
	if agentKey == "" {
		agentKey = s.deps.Registry.DefaultAgentKey()
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agent not found"))
		return
	}
	req.ChatID = chatID
	req.AgentKey = agentKey
	req.RequestID = requestID
	req.RunID = runID
	req.TeamID = teamID

	summary, created, err := s.deps.Chats.EnsureChat(chatID, agentKey, req.TeamID, req.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	session := engine.QuerySession{
		RequestID:             requestID,
		RunID:                 runID,
		ChatID:                chatID,
		ChatName:              summary.ChatName,
		AgentKey:              agentKey,
		AgentName:             agentDef.Name,
		ModelKey:              agentDef.ModelKey,
		ToolNames:             append([]string(nil), agentDef.Tools...),
		Mode:                  agentDef.Mode,
		TeamID:                req.TeamID,
		Created:               created,
		Budget:                cloneMap(agentDef.Budget),
		StageSettings:         cloneMap(agentDef.StageSettings),
		ToolOverrides:         cloneToolOverrides(agentDef.ToolOverrides),
		ResolvedBudget:        engine.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedStageSettings: engine.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
	}
	if principal := PrincipalFromContext(r.Context()); principal != nil {
		session.Subject = principal.Subject
	}
	runCtx, _, _ := s.deps.Runs.Register(r.Context(), session)
	defer s.deps.Runs.Finish(runID)

	agentStream, err := s.deps.Agent.Stream(runCtx, req, session)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer agentStream.Close()

	assembler := stream.NewAssembler(stream.StreamRequest{
		RequestID: requestID,
		RunID:     runID,
		ChatID:    chatID,
		ChatName:  summary.ChatName,
		AgentKey:  agentKey,
		Message:   req.Message,
		Role:      defaultRole(req.Role),
		Created:   created,
	})
	mapper := engine.NewDeltaMapper(runID, chatID, s.deps.Registry)

	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	var assistantText strings.Builder
	var reasoningText strings.Builder
	writeEvent := func(event stream.StreamEvent) error {
		data := event.ToData()
		if event.Type == "content.delta" {
			if delta, _ := data["delta"].(string); delta != "" {
				assistantText.WriteString(delta)
			}
		}
		if event.Type == "content.snapshot" {
			if text, _ := data["text"].(string); text != "" {
				assistantText.Reset()
				assistantText.WriteString(text)
			}
		}
		if event.Type == "reasoning.delta" {
			if delta, _ := data["delta"].(string); delta != "" {
				reasoningText.WriteString(delta)
			}
		}
		if event.Type == "reasoning.snapshot" {
			if text, _ := data["text"].(string); text != "" {
				reasoningText.Reset()
				reasoningText.WriteString(text)
			}
		}
		if stream.IsPersistedEventType(event.Type) {
			if err := s.deps.Chats.AppendEvent(chatID, data); err != nil {
				return err
			}
		}
		if event.Type == "tool.snapshot" && !s.deps.Config.SSE.IncludeToolPayloadEvents {
			return nil
		}
		return sseWriter.WriteJSON("message", data)
	}

	_ = s.deps.Chats.AppendRawMessage(chatID, map[string]any{
		"runId":   runID,
		"role":    defaultRole(req.Role),
		"content": req.Message,
		"ts":      now,
	})
	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	streamFailed := false
	streamInterrupted := false
	for {
		delta, err := agentStream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if engine.IsRunInterrupted(err) {
			streamInterrupted = true
			break
		}
		if err != nil {
			streamFailed = true
			for _, event := range assembler.Fail(err) {
				if writeErr := writeEvent(event); writeErr != nil {
					return
				}
			}
			break
		}
		inputs := mapper.Map(delta)
		for _, input := range inputs {
			for _, event := range assembler.Consume(input) {
				if err := writeEvent(event); err != nil {
					return
				}
			}
		}
	}

	if streamFailed {
		_ = sseWriter.WriteDone()
		return
	}
	if streamInterrupted {
		_ = sseWriter.WriteDone()
		return
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	finalAssistantText := assistantText.String()
	rawMsg := map[string]any{
		"runId":   runID,
		"role":    "assistant",
		"content": finalAssistantText,
		"ts":      time.Now().UnixMilli(),
	}
	if r := reasoningText.String(); r != "" {
		rawMsg["reasoning_content"] = r
	}
	_ = s.deps.Chats.AppendRawMessage(chatID, rawMsg)
	if err := s.deps.Chats.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AssistantText:   finalAssistantText,
		InitialMessage:  req.Message,
		UpdatedAtMillis: time.Now().UnixMilli(),
	}); err != nil {
		return
	}
	_ = sseWriter.WriteDone()
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" || req.ToolID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and toolId are required"))
		return
	}
	ack := s.deps.Runs.Submit(req)
	writeJSON(w, http.StatusOK, api.Success(api.SubmitResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		ToolID:   req.ToolID,
		Detail:   ack.Detail,
	}))
}

func (s *Server) handleSteer(w http.ResponseWriter, r *http.Request) {
	var req api.SteerRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" || strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and message are required"))
		return
	}
	ack := s.deps.Runs.Steer(req)
	writeJSON(w, http.StatusOK, api.Success(api.SteerResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		SteerID:  ack.SteerID,
		Detail:   ack.Detail,
	}))
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	var req api.InterruptRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId is required"))
		return
	}
	ack := s.deps.Runs.Interrupt(req)
	writeJSON(w, http.StatusOK, api.Success(api.InterruptResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    req.RunID,
		Detail:   ack.Detail,
	}))
}

func (s *Server) handleRemember(w http.ResponseWriter, r *http.Request) {
	var req api.RememberRequest
	if err := decodeJSON(r, &req); err != nil || req.RequestID == "" || req.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "requestId and chatId are required"))
		return
	}
	detail, err := s.deps.Chats.LoadChat(req.ChatID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	items, err := s.deps.Chats.ListChats("", "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	agentKey := ""
	for _, item := range items {
		if item.ChatID == req.ChatID {
			agentKey = item.AgentKey
			break
		}
	}
	response, err := s.deps.Memory.Remember(detail, req, agentKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleLearn(w http.ResponseWriter, r *http.Request) {
	var req api.LearnRequest
	if err := decodeJSON(r, &req); err != nil || req.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(api.LearnResponse{
		Accepted:  false,
		Status:    "not_connected",
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
	}))
}

func (s *Server) handleViewport(w http.ResponseWriter, r *http.Request) {
	viewportKey := r.URL.Query().Get("viewportKey")
	if viewportKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "viewportKey is required"))
		return
	}
	payload, err := s.deps.Viewport.Get(r.Context(), viewportKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(payload))
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	fileParam := r.URL.Query().Get("file")
	if fileParam == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "file is required"))
		return
	}
	if s.deps.Config.ChatImage.ResourceTicketEnabled {
		principal := PrincipalFromContext(r.Context())
		ticket := strings.TrimSpace(r.URL.Query().Get("t"))
		if principal == nil {
			if ticket == "" {
				writeJSON(w, http.StatusUnauthorized, api.Failure(http.StatusUnauthorized, "resource ticket required"))
				return
			}
			chatID, err := s.ticketService.Verify(ticket)
			if err != nil {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, err.Error()))
				return
			}
			if !resourceBelongsToChat(fileParam, chatID) {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket chat mismatch"))
				return
			}
		}
	}
	path, err := s.deps.Chats.ResolveResource(fileParam)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
			return
		}
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	http.ServeFile(w, r, path)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid multipart form"))
		return
	}
	requestID := strings.TrimSpace(r.FormValue("requestId"))
	if requestID == "" {
		requestID = newRunID()
	}
	chatID := strings.TrimSpace(r.FormValue("chatId"))
	if chatID == "" {
		chatID = newChatID()
	}
	_, _, err := s.deps.Chats.EnsureChat(chatID, s.deps.Registry.DefaultAgentKey(), "", r.FormValue("name"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	file, header, err := pickUploadFile(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()

	uploadID := "r01"
	targetName := safeFilename(header.Filename)
	targetPath := filepath.Join(s.deps.Chats.ChatDir(chatID), targetName)
	sum, size, err := saveUploadedFile(targetPath, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	resourceURL := "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(filepath.Join(chatID, targetName)))
	writeJSON(w, http.StatusOK, api.Success(api.UploadResponse{
		RequestID: requestID,
		ChatID:    chatID,
		Upload: api.UploadTicket{
			ID:        uploadID,
			Type:      "file",
			Name:      targetName,
			MimeType:  header.Header.Get("Content-Type"),
			SizeBytes: size,
			URL:       resourceURL,
			SHA256:    sum,
		},
	}))
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
		Model:       modelName,
		Mode:        def.Mode,
		Tools:       append([]string{}, def.Tools...),
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
	if def.Sandbox != nil {
		meta["sandbox"] = cloneMap(def.Sandbox)
	}
	return modelName, meta
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneListMaps(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, cloneMap(item))
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
			Parameters:    cloneMap(value.Parameters),
			Meta:          cloneMap(value.Meta),
		}
	}
	return out
}

func pickUploadFile(form *multipart.Form) (multipart.File, *multipart.FileHeader, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil, errors.New("file is required")
	}
	for _, headers := range form.File {
		if len(headers) == 0 {
			continue
		}
		file, err := headers[0].Open()
		return file, headers[0], err
	}
	return nil, nil, errors.New("file is required")
}

func saveUploadedFile(path string, src multipart.File) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	size, err := io.Copy(writer, src)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	return name
}

func newRunID() string {
	return chat.NewRunID()
}

func newChatID() string {
	return "chat_" + time.Now().UTC().Format("20060102150405.000000000")
}

func withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
