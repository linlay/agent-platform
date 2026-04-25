package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/memory"
	"agent-platform-runner-go/internal/stream"
)

// isHiddenRequest 判断请求是否标记为"系统自发触发"：
// 这类 run 不会在 chat 里留下用户回合（QueryLine 不写），
// 也不会广播 chat.created（避免 webclient 把它渲染成用户→agent 对话）。
// 典型来源：schedule 触发的定时任务。
func isHiddenRequest(req api.QueryRequest) bool {
	return req.Hidden != nil && *req.Hidden
}

type preparedQuery struct {
	req                api.QueryRequest
	summary            chat.Summary
	created            bool
	agentDef           catalog.AgentDefinition
	session            contracts.QuerySession
	memoryUsageSummary *api.MemoryUsageSummary
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
		// hidden run（schedule 等自发触发）也照常广播 chat.created —— 否则 webclient
		// 要刷新整个列表才能看到新建的 schedule 会话。隐藏语义只影响 chat 内部消息记录
		// （不写伪造的"用户发消息"），不影响会话在列表里的可见性。
		s.broadcast("chat.created", map[string]any{
			"chatId":    chatID,
			"chatName":  summary.ChatName,
			"agentKey":  agentKey,
			"timestamp": summary.CreatedAt,
		})
	}
	session, err := s.BuildQuerySession(r.Context(), req, summary, agentDef, querySessionBuildOptions{
		Created:           created,
		IncludeHistory:    !created,
		IncludeMemory:     true,
		AllowInvokeAgents: canUseInvokeAgentsTool(agentDef.Mode),
	})
	if err != nil {
		return preparedQuery{}, err
	}

	return preparedQuery{
		req:                req,
		summary:            summary,
		created:            created,
		agentDef:           agentDef,
		session:            session,
		memoryUsageSummary: session.MemoryUsageSummary,
	}, nil
}

func buildMemoryUsageSummary(staticMemoryPrompt string, bundle memory.ContextBundle) *api.MemoryUsageSummary {
	hitItems := buildMemoryHitItems(bundle)
	summary := &api.MemoryUsageSummary{
		HasStaticMemory:  strings.TrimSpace(staticMemoryPrompt) != "",
		StableCount:      len(bundle.StableFacts),
		SessionCount:     len(bundle.SessionSummaries),
		ObservationCount: len(bundle.RelevantObservations),
		StableChars:      len(strings.TrimSpace(bundle.StablePrompt)),
		SessionChars:     len(strings.TrimSpace(bundle.SessionPrompt)),
		ObservationChars: len(strings.TrimSpace(bundle.ObservationPrompt)),
		StableItems:      buildMemoryUsageItems(bundle.StableFacts),
		SessionItems:     buildMemoryUsageItems(bundle.SessionSummaries),
		ObservationItems: buildMemoryUsageItems(bundle.RelevantObservations),
		DisclosedLayers:  append([]string(nil), bundle.DisclosedLayers...),
		SnapshotID:       strings.TrimSpace(bundle.SnapshotID),
		StopReason:       strings.TrimSpace(bundle.StopReason),
		CandidateCounts:  cloneIntMap(bundle.CandidateCounts),
		SelectedCounts:   cloneIntMap(bundle.SelectedCounts),
	}
	summary.UserHint = buildMemoryUserHint(hitItems)
	if !summary.HasStaticMemory && summary.StableCount == 0 && summary.SessionCount == 0 && summary.ObservationCount == 0 {
		return nil
	}
	return summary
}

func buildMemoryUsageItems(items []api.StoredMemoryResponse) []api.MemoryUsageItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.MemoryUsageItem, 0, len(items))
	for _, item := range items {
		out = append(out, api.MemoryUsageItem{
			ID:        strings.TrimSpace(item.ID),
			Kind:      strings.TrimSpace(item.Kind),
			ScopeType: strings.TrimSpace(item.ScopeType),
			Title:     strings.TrimSpace(item.Title),
			Summary:   strings.TrimSpace(item.Summary),
			Category:  strings.TrimSpace(item.Category),
		})
	}
	return out
}

func buildMemoryHitItems(bundle memory.ContextBundle) []api.MemoryHitItem {
	out := make([]api.MemoryHitItem, 0, 3)
	appendHits := func(layer string, items []api.StoredMemoryResponse, limit int) {
		for _, item := range items {
			if limit > 0 && len(out) >= limit {
				return
			}
			out = append(out, api.MemoryHitItem{
				ID:        strings.TrimSpace(item.ID),
				Layer:     strings.TrimSpace(layer),
				Kind:      strings.TrimSpace(item.Kind),
				ScopeType: strings.TrimSpace(item.ScopeType),
				Title:     strings.TrimSpace(item.Title),
				Summary:   strings.TrimSpace(item.Summary),
				Category:  strings.TrimSpace(item.Category),
			})
		}
	}

	appendHits("stable", bundle.StableFacts, 3)
	appendHits("session", bundle.SessionSummaries, 3)
	appendHits("observation", bundle.RelevantObservations, 3)
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildMemoryUserHint(items []api.MemoryHitItem) string {
	if len(items) == 0 {
		return ""
	}
	labels := make([]string, 0, minInt(len(items), 3))
	for idx, item := range items {
		if idx >= 3 {
			break
		}
		label := strings.TrimSpace(item.Title)
		if label == "" {
			label = strings.TrimSpace(item.Summary)
		}
		if label == "" {
			continue
		}
		runes := []rune(label)
		if len(runes) > 24 {
			label = strings.TrimSpace(string(runes[:24])) + "..."
		}
		labels = append(labels, "《"+label+"》")
	}
	if len(labels) == 0 {
		return ""
	}
	return "本次回答借鉴了历史记忆：" + strings.Join(labels, "、")
}

func memoryUsageEventPayload(summary *api.MemoryUsageSummary, chatID string, runID string, agentKey string) map[string]any {
	if summary == nil {
		return nil
	}
	payload := map[string]any{
		"chatId":           strings.TrimSpace(chatID),
		"runId":            strings.TrimSpace(runID),
		"agentKey":         strings.TrimSpace(agentKey),
		"hasStaticMemory":  summary.HasStaticMemory,
		"stableCount":      summary.StableCount,
		"sessionCount":     summary.SessionCount,
		"observationCount": summary.ObservationCount,
		"stableChars":      summary.StableChars,
		"sessionChars":     summary.SessionChars,
		"observationChars": summary.ObservationChars,
	}
	if len(summary.StableItems) > 0 {
		payload["stableItems"] = summary.StableItems
	}
	if len(summary.SessionItems) > 0 {
		payload["sessionItems"] = summary.SessionItems
	}
	if len(summary.ObservationItems) > 0 {
		payload["observationItems"] = summary.ObservationItems
	}
	if strings.TrimSpace(summary.UserHint) != "" {
		payload["userHint"] = strings.TrimSpace(summary.UserHint)
	}
	if len(summary.DisclosedLayers) > 0 {
		payload["disclosedLayers"] = append([]string(nil), summary.DisclosedLayers...)
	}
	if strings.TrimSpace(summary.SnapshotID) != "" {
		payload["snapshotId"] = strings.TrimSpace(summary.SnapshotID)
	}
	if strings.TrimSpace(summary.StopReason) != "" {
		payload["stopReason"] = strings.TrimSpace(summary.StopReason)
	}
	if len(summary.CandidateCounts) > 0 {
		payload["candidateCounts"] = cloneIntMap(summary.CandidateCounts)
	}
	if len(summary.SelectedCounts) > 0 {
		payload["selectedCounts"] = cloneIntMap(summary.SelectedCounts)
	}
	return payload
}

func cloneIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func sandboxAgentEnv(value any) map[string]string {
	switch env := value.(type) {
	case map[string]string:
		return contracts.CloneStringMap(env)
	default:
		return nil
	}
}

func resolveSkillRuntimeSettings(agentEnv map[string]string, agentDir string, marketDir string, skillKeys []string) ([]string, map[string]string) {
	sandboxEnv := contracts.CloneStringMap(agentEnv)
	if len(skillKeys) == 0 {
		return nil, sandboxEnv
	}
	seen := map[string]struct{}{}
	var hookDirs []string
	for _, raw := range skillKeys {
		skillKey := strings.ToLower(strings.TrimSpace(raw))
		if skillKey == "" {
			continue
		}
		if _, ok := seen[skillKey]; ok {
			continue
		}
		seen[skillKey] = struct{}{}
		def, ok, err := catalog.ResolveSkillDefinition(agentDir, marketDir, skillKey)
		if err != nil {
			log.Printf("[server][skill-runtime][warn] skill resolution failed key=%s err=%v", skillKey, err)
			continue
		}
		if !ok {
			log.Printf("[server][skill-runtime][warn] skill definition not found key=%s", skillKey)
			continue
		}
		if strings.TrimSpace(def.BashHooksDir) != "" {
			hookDirs = append(hookDirs, def.BashHooksDir)
		}
		for key, value := range def.SandboxEnv {
			if sandboxEnv == nil {
				sandboxEnv = make(map[string]string, len(agentEnv)+len(def.SandboxEnv))
			}
			sandboxEnv[key] = value
		}
	}
	return hookDirs, sandboxEnv
}

func sortedStringKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) newAssemblerAndMapper(prepared preparedQuery) (*stream.StreamEventAssembler, *llm.DeltaMapper) {
	assembler := stream.NewAssembler(stream.StreamRequest{
		RequestID:          prepared.req.RequestID,
		RunID:              prepared.req.RunID,
		ChatID:             prepared.req.ChatID,
		ChatName:           prepared.summary.ChatName,
		AgentKey:           prepared.req.AgentKey,
		Message:            prepared.req.Message,
		Role:               defaultRole(prepared.req.Role),
		Created:            prepared.created,
		MemoryUsageSummary: memoryUsageEventPayload(prepared.memoryUsageSummary, prepared.req.ChatID, prepared.req.RunID, prepared.req.AgentKey),
	})
	if s.deps.Tools != nil {
		for _, toolDef := range s.deps.Tools.Definitions() {
			effective := applyToolOverride(toolDef, prepared.session.ToolOverrides)
			if cv, ok := effective.Meta["clientVisible"].(bool); ok && !cv {
				assembler.RegisterHiddenTools(effective.Name, effective.Key)
			}
		}
	}
	toolTimeoutMs := resolveHITLTimeoutFromBudget(prepared.session.ResolvedBudget, &s.deps.Config)
	mapper := llm.NewDeltaMapper(prepared.req.RunID, prepared.req.ChatID, toolTimeoutMs, s.toolLookupWithOverrides(prepared.session.ToolOverrides), s.deps.FrontendTools)
	return assembler, mapper
}

func resolveHITLTimeoutFromBudget(budget contracts.Budget, cfg *config.Config) int64 {
	normalized := contracts.NormalizeBudget(budget)
	if normalized.Hitl.TimeoutMs > 0 {
		return int64(normalized.Hitl.TimeoutMs)
	}
	if cfg != nil && cfg.BashHITL.DefaultTimeoutMs > 0 {
		return int64(cfg.BashHITL.DefaultTimeoutMs)
	}
	return 120000
}

func (s *Server) handleQueryAsync(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	runCtx, control, _ := s.deps.Runs.Register(r.Context(), prepared.session)
	principal := PrincipalFromContext(r.Context())
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
	defer observer.MarkDone()

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req))

	StartRunExecutor(RunExecutorParams{
		RunCtx:            runCtx,
		Request:           prepared.req,
		Session:           prepared.session,
		Summary:           prepared.summary,
		Agent:             s.deps.Agent,
		Registry:          s.deps.Registry,
		Assembler:         assembler,
		Mapper:            mapper,
		Stream:            s.deps.Config.Stream,
		StepWriter:        stepWriter,
		EventBus:          eventBus,
		Chats:             s.deps.Chats,
		RunControl:        control,
		BuildQuerySession: s.BuildQuerySession,
		Notifications:     s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
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

	s.broadcast("run.started", map[string]any{
		"runId":    prepared.req.RunID,
		"chatId":   prepared.req.ChatID,
		"agentKey": prepared.req.AgentKey,
	})
	defer s.broadcast("run.finished", map[string]any{
		"runId":  prepared.req.RunID,
		"chatId": prepared.req.ChatID,
	})

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	principal := &Principal{Subject: prepared.session.Subject}
	if strings.TrimSpace(principal.Subject) == "" {
		principal = nil
	}
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
		stepWriter:    chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req)),
		stream:        s.deps.Config.Stream,
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
		persisted, completion := persistRunCompletionIfNeeded(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
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
		persisted, completion := persistRunCompletionIfNeeded(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, false)
		if persisted {
			syncBroadcastChatUpdated(s.deps.Notifications, completion)
		}
		_ = sseWriter.WriteDone()
		return
	}

	for _, event := range assembler.Complete() {
		if err := writeEvent(event); err != nil {
			return
		}
	}
	persisted, completion := persistRunCompletionIfNeeded(syncRunExecutorParams(s, prepared, control, principal), assistantText.String(), runUsage, true)
	if persisted {
		syncBroadcastChatUpdated(s.deps.Notifications, completion)
	}
	_ = sseWriter.WriteDone()
}

// syncRunExecutorParams 构造 handleQuerySync 三次 persistRunCompletionIfNeeded
// 调用共用的 RunExecutorParams，避免重复拼装三份 callback。
func syncRunExecutorParams(s *Server, prepared preparedQuery, control *contracts.RunControl, principal *Principal) RunExecutorParams {
	return RunExecutorParams{
		Request:       prepared.req,
		Session:       prepared.session,
		Chats:         s.deps.Chats,
		RunControl:    control,
		Notifications: s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
	}
}

// syncBroadcastChatUpdated 复刻 run_executor.broadcastRunCompletion 的 chat.updated
// 广播语义。async 路径在 StartRunExecutor 内部走那条；sync 路径没经过 StartRunExecutor，
// 这里手动补上，让 schedule 触发的 run 也能通知 hub（进而透传到 gateway / webclient）。
func syncBroadcastChatUpdated(notifications contracts.NotificationSink, completion chat.RunCompletion) {
	if notifications == nil {
		return
	}
	notifications.Broadcast("chat.updated", map[string]any{
		"chatId":         completion.ChatID,
		"lastRunId":      completion.RunID,
		"lastRunContent": completion.AssistantText,
		"updatedAt":      completion.UpdatedAtMillis,
	})
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req api.SubmitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid submit payload"))
		return
	}
	if response, ok := s.forwardProxySubmit(req); ok {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	response, _, _, err := s.resolveSubmit(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
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
	if response, ok := s.forwardProxyInterrupt(req); ok {
		writeJSON(w, http.StatusOK, api.Success(response))
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
