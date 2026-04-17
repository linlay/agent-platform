package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
)

type preparedQuery struct {
	req      api.QueryRequest
	summary  chat.Summary
	created  bool
	agentDef catalog.AgentDefinition
	session  contracts.QuerySession
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	prepared, err := s.prepareQuery(r)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			writeJSON(w, statusErr.status, api.Failure(statusErr.status, statusErr.message))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if strings.EqualFold(prepared.agentDef.Mode, "PROXY") {
		s.handleProxyQuery(w, r, prepared.req, prepared.agentDef)
		return
	}
	if isSyncQueryContext(r.Context()) {
		s.handleQuerySync(w, r.Context(), prepared)
		return
	}
	s.handleQueryAsync(w, r, prepared)
}

type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (s *Server) prepareQuery(r *http.Request) (preparedQuery, error) {
	var req api.QueryRequest
	if err := decodeJSON(r, &req); err != nil {
		return preparedQuery{}, &statusError{status: http.StatusBadRequest, message: "invalid request body"}
	}
	if strings.TrimSpace(req.Message) == "" {
		return preparedQuery{}, &statusError{status: http.StatusBadRequest, message: "message is required"}
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
		return preparedQuery{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}

	req.ChatID = chatID
	req.AgentKey = agentKey
	req.RequestID = requestID
	req.RunID = runID
	req.TeamID = teamID

	summary, created, err := s.deps.Chats.EnsureChat(chatID, agentKey, req.TeamID, req.Message)
	if err != nil {
		return preparedQuery{}, err
	}
	if created {
		s.broadcast("chat.created", map[string]any{
			"chatId":    chatID,
			"chatName":  summary.ChatName,
			"agentKey":  agentKey,
			"timestamp": summary.CreatedAt,
		})
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
		return preparedQuery{}, err
	}

	promptAppend := buildPromptAppendConfig(agentDef)
	skillHookDirs, sandboxEnvOverrides := resolveSkillRuntimeSettings(agentDef.Skills, s.deps.Registry)
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
		Budget:                contracts.CloneMap(agentDef.Budget),
		StageSettings:         contracts.CloneMap(agentDef.StageSettings),
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
		SkillHookDirs:         skillHookDirs,
		SandboxEnvOverrides:   sandboxEnvOverrides,
	}
	if principal != nil {
		session.Subject = principal.Subject
	}

	return preparedQuery{
		req:      req,
		summary:  summary,
		created:  created,
		agentDef: agentDef,
		session:  session,
	}, nil
}

func resolveSkillRuntimeSettings(skillKeys []string, registry catalog.Registry) ([]string, map[string]string) {
	if len(skillKeys) == 0 || registry == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	var hookDirs []string
	var sandboxEnv map[string]string
	for _, raw := range skillKeys {
		skillKey := strings.ToLower(strings.TrimSpace(raw))
		if skillKey == "" {
			continue
		}
		if _, ok := seen[skillKey]; ok {
			continue
		}
		seen[skillKey] = struct{}{}
		def, ok := registry.SkillDefinition(skillKey)
		if !ok {
			continue
		}
		if strings.TrimSpace(def.BashHooksDir) != "" {
			hookDirs = append(hookDirs, def.BashHooksDir)
		}
		for key, value := range def.SandboxEnv {
			if sandboxEnv == nil {
				sandboxEnv = make(map[string]string, len(def.SandboxEnv))
			}
			sandboxEnv[key] = value
		}
	}
	return hookDirs, sandboxEnv
}

func (s *Server) newAssemblerAndMapper(prepared preparedQuery) (*stream.StreamEventAssembler, *llm.DeltaMapper) {
	assembler := stream.NewAssembler(stream.StreamRequest{
		RequestID: prepared.req.RequestID,
		RunID:     prepared.req.RunID,
		ChatID:    prepared.req.ChatID,
		ChatName:  prepared.summary.ChatName,
		AgentKey:  prepared.req.AgentKey,
		Message:   prepared.req.Message,
		Role:      defaultRole(prepared.req.Role),
		Created:   prepared.created,
	})
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			effective := applyToolOverride(toolDef, prepared.session.ToolOverrides)
			if cv, ok := effective.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(effective.Name, effective.Key)
			}
		}
	}
	toolTimeoutMs := int64(contracts.NormalizeBudget(prepared.session.ResolvedBudget).Tool.TimeoutMs)
	mapper := llm.NewDeltaMapper(prepared.req.RunID, prepared.req.ChatID, toolTimeoutMs, s.toolLookup(), s.deps.FrontendTools)
	return assembler, mapper
}

func (s *Server) handleQueryAsync(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	runCtx, control, _ := s.deps.Runs.Register(r.Context(), prepared.session)
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run event bus unavailable"))
		return
	}
	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})

	sseWriter, err := stream.NewWriter(w, stream.Options{
		SSE:            s.deps.Config.SSE,
		Render:         s.deps.Config.H2A.Render,
		LoggingEnabled: s.deps.Config.Logging.SSE.Enabled,
	})
	if err != nil {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer sseWriter.Close()
	sseWriter.StartHeartbeat()

	observer, err := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if err != nil {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)

	StartRunExecutor(RunExecutorParams{
		RunCtx:        runCtx,
		Request:       prepared.req,
		Session:       prepared.session,
		Summary:       prepared.summary,
		Agent:         s.deps.Agent,
		Assembler:     assembler,
		Mapper:        mapper,
		StepWriter:    stepWriter,
		EventBus:      eventBus,
		Chats:         s.deps.Chats,
		RunControl:    control,
		Notifications: s.deps.Notifications,
		OnComplete: func(runID string) {
			s.deps.Runs.Finish(runID)
			s.broadcast("run.finished", map[string]any{
				"runId":  runID,
				"chatId": prepared.req.ChatID,
			})
		},
	})

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-observer.Events:
			if !ok {
				_ = sseWriter.WriteDone()
				return
			}
			if err := sseWriter.WriteJSON("message", event); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleQuerySync(w http.ResponseWriter, ctx context.Context, prepared preparedQuery) {
	control := contracts.NewRunControl(ctx, prepared.req.RunID)
	control.SetObserverCount(1)
	runCtx := contracts.WithRunControl(control.Context(), control)
	defer control.SetObserverCount(0)

	assembler, mapper := s.newAssemblerAndMapper(prepared)
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

	var (
		assistantText strings.Builder
		chatUsage     chat.UsageData
		runUsage      chat.UsageData
	)
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	processor := &runEventProcessor{
		assistantText: &assistantText,
		stepWriter:    chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode),
		chatUsage:     chatUsage,
		runUsage:      &runUsage,
	}
	writeEvent := func(event stream.StreamEvent) error {
		data, visible := processor.Consume(event)
		if !visible {
			return nil
		}
		return sseWriter.WriteJSON("message", data)
	}

	for _, event := range assembler.Bootstrap() {
		if err := writeEvent(event); err != nil {
			return
		}
	}

	agentStream, err := s.deps.Agent.Stream(runCtx, prepared.req, prepared.session)
	if err != nil {
		control.TransitionState(contracts.RunLoopStateFailed)
		for _, event := range assembler.Fail(err) {
			_ = writeEvent(event)
		}
		persistRunCompletionIfNeeded(RunExecutorParams{
			Request:       prepared.req,
			Session:       prepared.session,
			Chats:         s.deps.Chats,
			RunControl:    control,
			Notifications: s.deps.Notifications,
		}, assistantText.String(), runUsage, false)
		_ = sseWriter.WriteDone()
		return
	}
	defer agentStream.Close()

	streamFailed := false
	streamInterrupted := false
	for {
		delta, nextErr := agentStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			streamInterrupted = true
			break
		}
		if nextErr != nil {
			streamFailed = true
			control.TransitionState(contracts.RunLoopStateFailed)
			for _, event := range assembler.Fail(nextErr) {
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

	processor.stepWriter.Flush()
	if streamFailed || streamInterrupted {
		persistRunCompletionIfNeeded(RunExecutorParams{
			Request:       prepared.req,
			Session:       prepared.session,
			Chats:         s.deps.Chats,
			RunControl:    control,
			Notifications: s.deps.Notifications,
		}, assistantText.String(), runUsage, false)
		_ = sseWriter.WriteDone()
		return
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return
		}
	}
	persistRunCompletionIfNeeded(RunExecutorParams{
		Request:       prepared.req,
		Session:       prepared.session,
		Chats:         s.deps.Chats,
		RunControl:    control,
		Notifications: s.deps.Notifications,
	}, assistantText.String(), runUsage, true)
	_ = sseWriter.WriteDone()
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRequest
	if err := decodeJSON(r, &req); err != nil || req.RunID == "" || req.AwaitingID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "runId and awaitingId are required"))
		return
	}
	ack := s.deps.Runs.Submit(req)
	writeJSON(w, http.StatusOK, api.Success(api.SubmitResponse{
		Accepted:   ack.Accepted,
		Status:     ack.Status,
		RunID:      req.RunID,
		AwaitingID: req.AwaitingID,
		Detail:     ack.Detail,
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
