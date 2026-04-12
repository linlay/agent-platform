package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
)

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

	var historyMessages []map[string]any
	if !created {
		historyMessages, _ = s.deps.Chats.LoadRawMessages(chatID, s.deps.Config.ChatStorage.K)
	}

	var memoryContext string
	if s.deps.Memory != nil && req.Message != "" {
		topN := s.deps.Config.Memory.ContextTopN
		if topN <= 0 {
			topN = 5
		}
		maxChars := s.deps.Config.Memory.ContextMaxChars
		if maxChars <= 0 {
			maxChars = 4000
		}
		memories, _ := s.deps.Memory.Search(req.Message, topN)
		if len(memories) > 0 {
			var sb strings.Builder
			for _, mem := range memories {
				entry := fmt.Sprintf("id: %s\nsubjectKey: %s\nsourceType: %s\ncategory: %s\nimportance: %d\ntags: %s\ncontent: %s\n---\n",
					mem.ID, mem.SubjectKey, mem.SourceType, mem.Category, mem.Importance,
					strings.Join(mem.Tags, ","), mem.Summary)
				if sb.Len()+len(entry) > maxChars {
					break
				}
				sb.WriteString(entry)
			}
			memoryContext = sb.String()
		}
	}

	principal := PrincipalFromContext(r.Context())
	runtimeContext, err := s.buildRuntimeRequestContext(runtimeRequestContextInput{
		agentKey:   agentKey,
		teamID:     req.TeamID,
		role:       defaultRole(req.Role),
		chatID:     chatID,
		chatName:   summary.ChatName,
		scene:      req.Scene,
		references: req.References,
		principal:  principal,
		definition: agentDef,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if strings.EqualFold(agentDef.Mode, "PROXY") {
		s.handleProxyQuery(w, r, req, agentDef)
		return
	}

	promptAppend := buildPromptAppendConfig(agentDef)
	session := contracts.QuerySession{
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
		SkillKeys:             append([]string(nil), agentDef.Skills...),
		ContextTags:           append([]string(nil), agentDef.ContextTags...),
		Budget:                cloneMap(agentDef.Budget),
		StageSettings:         cloneMap(agentDef.StageSettings),
		ToolOverrides:         cloneToolOverrides(agentDef.ToolOverrides),
		ResolvedBudget:        contracts.ResolveBudget(s.deps.Config, agentDef.Budget),
		ResolvedStageSettings: contracts.ResolvePlanExecuteSettings(agentDef.StageSettings, s.deps.Config.Defaults.Plan.MaxSteps, s.deps.Config.Defaults.Plan.MaxWorkRoundsPerTask),
		HistoryMessages:       historyMessages,
		MemoryContext:         memoryContext,
		RuntimeContext:        runtimeContext,
		PromptAppend:          promptAppend,
		MemoryPrompt:          agentDef.MemoryPrompt,
		SkillCatalogPrompt:    buildSkillCatalogPrompt(agentDef, s.deps.Registry, promptAppend),
		SoulPrompt:            agentDef.SoulPrompt,
		AgentsPrompt:          agentDef.AgentsPrompt,
		PlanPrompt:            agentDef.PlanPrompt,
		ExecutePrompt:         agentDef.ExecutePrompt,
		SummaryPrompt:         agentDef.SummaryPrompt,
		SandboxEnvironmentID:  extractSandboxField(agentDef.Sandbox, "environmentId"),
		SandboxLevel:          extractSandboxField(agentDef.Sandbox, "level"),
		SandboxExtraMounts:    sandboxExtraMounts(agentDef.Sandbox["extraMounts"]),
	}
	if principal != nil {
		session.Subject = principal.Subject
	}
	runCtx, _, _ := s.deps.Runs.Register(r.Context(), session)
	defer s.deps.Runs.Finish(runID)

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
	toolTimeout := int64(session.ResolvedBudget.Tool.TimeoutMs)
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			effective := applyToolOverride(toolDef, session.ToolOverrides)
			if cv, ok := effective.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(effective.Name, effective.Key)
			}
			if toolType, viewportKey, ok := frontendToolMetadata(effective); ok {
				assembler.RegisterFrontendTool(effective.Name, toolType, viewportKey, toolTimeout)
				assembler.RegisterFrontendTool(effective.Key, toolType, viewportKey, toolTimeout)
			}
		}
	}
	var toolLookup contracts.ToolDefinitionLookup = s.deps.Registry
	if tl, ok := s.deps.Tools.(contracts.ToolDefinitionLookup); ok {
		toolLookup = contracts.NewCompositeToolLookup(s.deps.Registry, tl)
	}
	mapper := llm.NewDeltaMapper(runID, chatID, toolLookup)

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
	stepWriter := chat.NewStepWriter(s.deps.Chats, chatID, runID, agentDef.Mode)
	writeEvent := func(event stream.StreamEvent) error {
		data := event.Data()
		if event.Type == "content.delta" {
			if delta := data.String("delta"); delta != "" {
				assistantText.WriteString(delta)
			}
		}
		if event.Type == "content.snapshot" {
			if text := data.String("text"); text != "" {
				assistantText.Reset()
				assistantText.WriteString(text)
			}
		}
		stepWriter.OnEvent(data)
		if event.Type == "stage.marker" {
			return nil
		}
		if strings.HasSuffix(event.Type, ".snapshot") {
			return nil
		}
		return sseWriter.WriteJSON("message", data)
	}

	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	agentStream, err := s.deps.Agent.Stream(runCtx, req, session)
	if err != nil {
		for _, event := range assembler.Fail(err) {
			_ = writeEvent(event)
		}
		_ = sseWriter.WriteDone()
		return
	}
	defer agentStream.Close()

	streamFailed := false
	streamInterrupted := false
	for {
		delta, err := agentStream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(err) {
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

	if streamFailed || streamInterrupted {
		stepWriter.Flush()
		_ = sseWriter.WriteDone()
		return
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	finalAssistantText := assistantText.String()
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
