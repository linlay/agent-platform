package acp

import (
	"agent-platform/internal/agentengine"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/terminal"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/coder/acp-go-sdk"
	"log"
	"path/filepath"
	"sync"
	"time"
)

// Sessions owns Chat-bound ACP state. HTTP ownership is checked at the existing
// transport boundary; engine and workspace are resolved from the stored Agent.
type Sessions struct {
	previews       map[string]configPreview
	previewPending *previewOperation
	previewEpoch   uint64
	slots          chan struct{}
	workerTasks    sync.WaitGroup
	reaperDone     chan struct{}
	workersMu      sync.Mutex
	workers        map[string]*engineWorker
	operations     sync.WaitGroup
	host           *Supervisor
	chats          *chat.FileStore
	resolve        func(string) (catalog.AgentDefinition, bool)
	mu             sync.Mutex
	entries        map[string]*session
	preparations   map[string]*preparation
	closed         bool
	auth           map[string]*authentication
	authTerminals  *terminal.Manager
}
type preparation struct {
	done   chan struct{}
	result dto.PreparedSession
	err    error
}

type SessionDetails struct {
	Capabilities      json.RawMessage
	AvailableCommands []json.RawMessage
	CurrentModeID     string
}
type session struct {
	preview                               bool
	lastUsed                              time.Time
	backgroundPossible                    bool
	pendingTools                          map[string]bool
	childDefinition                       *catalog.AgentDefinition
	details                               SessionDetails
	preparing, readyState, configNotified bool
	generation, observedSessionID         string
	sessionID                             string
	handler                               Handler
	turnCtx                               context.Context
	turnCalls                             sync.WaitGroup
	pendingInteractions                   int
	op                                    sync.Mutex
	mu                                    sync.Mutex
	process                               *sessionProcess
	binding                               dto.SessionBinding
	prepared                              dto.PreparedSession
}

func NewSessions(host *Supervisor, chats *chat.FileStore, resolve func(string) (catalog.AgentDefinition, bool)) *Sessions {
	s := &Sessions{host: host, chats: chats, resolve: resolve, entries: map[string]*session{}, preparations: map[string]*preparation{}, workers: map[string]*engineWorker{}, slots: make(chan struct{}, host.options.MaxSessions), reaperDone: make(chan struct{})}
	go s.reapIdle()
	return s
}
func (s *Sessions) acquire(chatID string) (*session, error) {
	if !chat.ValidChatID(chatID) {
		return nil, agentengine.Error("invalid_arguments")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	e := s.entries[chatID]
	if e == nil {
		e = &session{}
		s.entries[chatID] = e
	}
	if !e.op.TryLock() {
		log.Printf("[acp] admission rejected reason=session_operation_busy")
		return nil, ErrBusy
	}
	s.operations.Add(1)
	return e, nil
}
func (s *Sessions) release(chatID string, e *session) {
	defer s.operations.Done()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.process == nil {
		delete(s.entries, chatID)
	}
	e.mu.Lock()
	e.lastUsed = time.Now()
	e.mu.Unlock()
	e.op.Unlock()
}

func (s *Sessions) Inspect(chatID string) (dto.PreparedSession, error) {
	s.mu.Lock()
	e := s.entries[chatID]
	s.mu.Unlock()
	if e == nil {
		return dto.PreparedSession{}, agentengine.Error("session_configuration_stale")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.readyState || e.process == nil {
		return dto.PreparedSession{}, agentengine.Error("session_configuration_stale")
	}
	select {
	case <-e.process.Done():
		return dto.PreparedSession{}, agentengine.Error("session_configuration_stale")
	default:
	}
	return copyPrepared(e.prepared), nil
}
func copyPrepared(p dto.PreparedSession) dto.PreparedSession {
	b, _ := json.Marshal(p)
	var result dto.PreparedSession
	json.Unmarshal(b, &result)
	return result
}
func (s *Sessions) target(chatID string) (string, string, error) {
	summary, err := s.chats.Summary(chatID)
	if err != nil {
		return "", "", err
	}
	if summary == nil {
		return "", "", chat.ErrChatNotFound
	}
	def, ok := s.resolve(summary.AgentKey)
	if !ok || def.Engine != catalog.AgentEngineACP {
		return "", "", agentengine.Error("engine_unavailable")
	}
	return s.ValidateTarget(def)
}

// ValidateTarget checks catalog readiness and Workspace without spawning.
func (s *Sessions) ValidateTarget(def catalog.AgentDefinition) (string, string, error) {
	if def.Engine != catalog.AgentEngineACP {
		return "", "", agentengine.Error("engine_unavailable")
	}
	if err := catalog.ValidateProjectWorkspace(map[string]any{"runtimeConfig": map[string]any{"workspaceRoot": def.Workspace.ProjectDir()}}); err != nil {
		return "", "", agentengine.Error("invalid_arguments")
	}
	engineID, _ := def.Runtime["engineId"].(string)
	workspace, err := filepath.EvalSymlinks(def.Workspace.ProjectDir())
	if err != nil {
		return "", "", agentengine.Error("invalid_arguments")
	}
	directory := s.host.registry.Directory()
	if !directory.Ready {
		return "", "", agentengine.Error("registry_not_ready")
	}
	for _, engine := range directory.Engines {
		if engine.EngineID == engineID && engine.Enabled {
			return engineID, workspace, nil
		}
	}
	return "", "", agentengine.Error("engine_unavailable")
}
func (s *Sessions) Prepare(ctx context.Context, chatID string) (dto.PreparedSession, error) {
	if err := ctx.Err(); err != nil {
		return dto.PreparedSession{}, err
	}
	if _, _, err := s.target(chatID); err != nil {
		return dto.PreparedSession{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return dto.PreparedSession{}, ErrClosed
	}
	pending := s.preparations[chatID]
	if pending == nil {
		if len(s.preparations) >= s.host.options.MaxQueued {
			log.Printf("[acp] admission rejected reason=preparation_limit queued=%d limit=%d", len(s.preparations), s.host.options.MaxQueued)
			s.mu.Unlock()
			return dto.PreparedSession{}, ErrBusy
		}
		pending = &preparation{done: make(chan struct{})}
		s.preparations[chatID] = pending
		s.operations.Add(1)
		go func() {
			defer s.operations.Done()
			// Navigation cancels a caller's wait, not the shared preparation.
			// The existing request budget and host shutdown still bound its lifetime.
			prepareCtx, cancel := context.WithTimeout(s.host.ctx, s.host.options.RequestTimeout)
			defer cancel()
			pending.result, pending.err = s.prepare(prepareCtx, chatID, nil)
			s.mu.Lock()
			delete(s.preparations, chatID)
			close(pending.done)
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return dto.PreparedSession{}, ctx.Err()
	case <-pending.done:
		if pending.err != nil {
			return dto.PreparedSession{}, pending.err
		}
		// A revoke while waiting remains authoritative.
		if _, _, err := s.target(chatID); err != nil {
			return dto.PreparedSession{}, err
		}
		return copyPrepared(pending.result), nil
	}
}

// PrepareChild isolates a leased child definition from its parent's durable binding.
// The scope exists only for this Run/task and is closed with the child stream.
func (s *Sessions) PrepareChild(ctx context.Context, chatID, runID, taskID string, def catalog.AgentDefinition) (dto.PreparedSession, error) {
	parent, err := s.chats.Summary(chatID)
	if err != nil {
		return dto.PreparedSession{}, err
	}
	if parent == nil || runID == "" || taskID == "" {
		return dto.PreparedSession{}, agentengine.Error("invalid_arguments")
	}
	scope := fmt.Sprintf("acp-child-%x", sha256.Sum256([]byte(chatID+"\x00"+runID+"\x00"+taskID)))
	return s.prepare(ctx, scope, &def)
}
func (s *Sessions) saveBinding(e *session) error {
	if e.childDefinition != nil {
		return nil
	}
	return s.chats.SaveEngineSession(e.binding)
}
func (s *Sessions) prepare(ctx context.Context, chatID string, child *catalog.AgentDefinition) (dto.PreparedSession, error) {
	return s.prepareSession(ctx, chatID, child, false)
}

func (s *Sessions) prepareSession(ctx context.Context, chatID string, child *catalog.AgentDefinition, preview bool) (dto.PreparedSession, error) {
	var engineID, workspace string
	var err error
	if child != nil {
		engineID, workspace, err = s.ValidateTarget(*child)
	} else {
		engineID, workspace, err = s.target(chatID)
	}
	if err != nil {
		return dto.PreparedSession{}, err
	}
	// Revocation is authoritative even while the previous process is draining.
	e, err := s.acquire(chatID)
	if err != nil {
		return dto.PreparedSession{}, err
	}
	defer s.release(chatID, e)
	e.childDefinition = child
	e.preview = preview
	s.mu.Lock()
	auth := s.auth[engineID]
	authenticating := auth != nil && auth.state == "authenticating"
	s.mu.Unlock()
	if authenticating {
		return dto.PreparedSession{}, agentengine.Error("auth_required")
	}

	if e.process != nil {
		if e.process.worker.draining() {
			if err := e.process.Close(); err != nil {
				return dto.PreparedSession{}, err
			}
		}
		select {
		case <-e.process.retired:
			if err := e.process.Close(); err != nil {
				return dto.PreparedSession{}, err
			}
		default:
		}
		select {
		case <-e.process.Done():
			e.mu.Lock()
			e.process = nil
			e.readyState = false
			e.mu.Unlock()
		default:
			if e.binding.EngineID != engineID || e.binding.WorkspaceRoot != workspace {
				return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
			}
			return s.Inspect(chatID)
		}
	}
	// Capture the scope before startup so concurrent catalog/auth changes cannot
	// publish old defaults under a new cache identity.
	previewKey := ""
	if child == nil {
		if summary, err := s.chats.Summary(chatID); err == nil && summary != nil {
			if def, ok := s.resolve(summary.AgentKey); ok {
				s.mu.Lock()
				previewKey = s.previewKey(engineID, workspace, def)
				s.mu.Unlock()
			}
		}
	}
	var binding *dto.SessionBinding
	if child == nil {
		binding, err = s.chats.LoadEngineSession(chatID)
	}
	if err != nil {
		return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
	}
	if binding == nil && child == nil {
		summary, err := s.chats.Summary(chatID)
		if err != nil {
			return dto.PreparedSession{}, err
		}
		if summary == nil {
			return dto.PreparedSession{}, chat.ErrChatNotFound
		}
		if summary.LastRunID != "" {
			return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
		}
	}
	if binding != nil && (binding.EngineID != engineID || binding.WorkspaceRoot != workspace || binding.ProfileID != profileID(engineID)) {
		return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
	}
	e.mu.Lock()
	e.preparing = true
	e.readyState = false
	e.configNotified = false
	e.observedSessionID = ""
	e.details = SessionDetails{}
	e.prepared = dto.PreparedSession{ConfigOptions: []dto.ExternalConfigOption{}}
	if binding != nil {
		e.sessionID = binding.ACPSessionID
	} else {
		e.sessionID = ""
	}
	e.mu.Unlock()
	p, raw, finishSetup, err := s.startSessionProcess(ctx, chatID, engineID, workspace, e)
	for attempts := 0; !preview && errors.Is(err, ErrBusy) && s.host.Stats().Processes >= s.host.options.MaxProcesses && attempts < s.host.options.MaxSessions; attempts++ {
		if !s.closeIdleWorkers(time.Time{}) {
			if err = s.releaseIdleSession(time.Time{}); err != nil {
				break
			}
			s.closeIdleWorkers(time.Time{})
		}
		p, raw, finishSetup, err = s.startSessionProcess(ctx, chatID, engineID, workspace, e)
	}

	if err != nil {
		if errors.Is(err, ErrBusy) {
			stats := s.host.Stats()
			log.Printf("[acp] admission rejected reason=no_reclaimable_capacity processes=%d/%d sessions=%d/%d active=%d queued=%d", stats.Processes, s.host.options.MaxProcesses, len(s.slots), s.host.options.MaxSessions, stats.Active, stats.Queued)
		}
		return dto.PreparedSession{}, err
	}
	defer finishSetup()
	e.mu.Lock()
	e.process = p
	e.generation = p.Generation()
	e.mu.Unlock()
	keep := false
	defer func() {
		if !keep {
			p.Close()
			e.mu.Lock()
			e.process = nil
			e.readyState = false
			e.preparing = false
			e.mu.Unlock()
		}
	}()
	s.observeAuth(engineID, raw, "")
	var initRaw struct {
		AgentCapabilities json.RawMessage `json:"agentCapabilities"`
	}
	json.Unmarshal(raw, &initRaw)
	e.mu.Lock()
	e.details.Capabilities = initRaw.AgentCapabilities
	e.mu.Unlock()
	var init struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(raw, &init) != nil || init.ProtocolVersion != 1 {
		return dto.PreparedSession{}, agentengine.Error("capability_unsupported")
	}
	method := "session/new"
	if binding != nil {
		var resume map[string]json.RawMessage
		if json.Unmarshal(init.AgentCapabilities.SessionCapabilities.Resume, &resume) == nil && resume != nil {
			method = "session/resume"
		} else if init.AgentCapabilities.LoadSession {
			method = "session/load"
		}
		if method == "session/new" && binding.ContextState != "prepared" {
			return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
		}
	}
	params := map[string]any{"cwd": workspace, "mcpServers": []any{}}
	var capabilities sdk.AgentCapabilities
	json.Unmarshal(initRaw.AgentCapabilities, &capabilities)
	if capabilities.McpCapabilities.Http {
		bridge, err := startPlatformMCP(e, p)
		if err != nil {
			return dto.PreparedSession{}, err
		}
		params["mcpServers"] = []any{bridge}
	}
	if method != "session/new" {
		params["sessionId"] = binding.ACPSessionID
	}
	raw, err = controlRequest(ctx, p, method, params)
	if errors.Is(err, agentengine.Error("auth_required")) {
		s.observeAuth(engineID, nil, "required")
		p.worker.invalidate()
		return dto.PreparedSession{}, err
	}
	if err != nil && binding != nil {
		if binding.ContextState != "prepared" {
			return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
		}
		if ctx.Err() != nil {
			return dto.PreparedSession{}, ctx.Err()
		}
		method = "session/new"
		e.mu.Lock()
		e.sessionID = ""
		e.observedSessionID = ""
		e.configNotified = false
		e.prepared.ConfigOptions = []dto.ExternalConfigOption{}
		e.details.AvailableCommands = nil
		e.details.CurrentModeID = ""
		e.mu.Unlock()
		delete(params, "sessionId")
		raw, err = controlRequest(ctx, p, method, params)
	}
	if err != nil {
		return dto.PreparedSession{}, err
	}
	invalidResponse := agentengine.Error("rpc_connection_lost")
	if method != "session/new" {
		invalidResponse = "session_restore_failed"
	}
	var result struct {
		SessionID     string            `json:"sessionId"`
		ConfigOptions []json.RawMessage `json:"configOptions"`
		Modes         struct {
			CurrentModeID string `json:"currentModeId"`
		} `json:"modes"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return dto.PreparedSession{}, invalidResponse
	}
	if method != "session/new" {
		if result.SessionID != "" && result.SessionID != binding.ACPSessionID {
			return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
		}
		result.SessionID = binding.ACPSessionID
	}
	if result.SessionID == "" {
		return dto.PreparedSession{}, invalidResponse
	}
	options, err := configOptions(result.ConfigOptions)
	if err != nil {
		if method != "session/new" {
			return dto.PreparedSession{}, invalidResponse
		}
		return dto.PreparedSession{}, err
	}
	if !p.uniqueSessionID(result.SessionID) {
		p.stop()
		return dto.PreparedSession{}, invalidResponse
	}
	e.mu.Lock()
	if e.observedSessionID != "" && e.observedSessionID != result.SessionID {
		e.mu.Unlock()
		return dto.PreparedSession{}, invalidResponse
	}
	if e.configNotified {
		options = e.prepared.ConfigOptions
	}
	if e.details.CurrentModeID == "" {
		e.details.CurrentModeID = result.Modes.CurrentModeID
	}
	e.binding = dto.SessionBinding{SchemaVersion: 1, ChatID: chatID, EngineID: engineID, ProfileID: profileID(engineID), WorkspaceRoot: workspace, ACPSessionID: result.SessionID, ContextState: "prepared", ConfigSelection: confirmedSelections(options)}
	if method != "session/new" {
		e.binding = *binding
	}
	e.sessionID = result.SessionID
	e.prepared = dto.PreparedSession{ChatID: chatID, EngineID: engineID, PreparationID: p.Generation(), ConfigRevision: 1, ConfigOptions: options}
	e.mu.Unlock()
	e.binding.LastUsed = &struct {
		PluginVersion  string `json:"pluginVersion"`
		ArtifactDigest string `json:"artifactDigest"`
	}{p.pluginVersion, p.artifactDigest}
	defaults := clonePreview(dto.ConfigPreview{EngineID: engineID, ConfigOptions: options})
	if binding != nil && len(binding.ConfigSelection) > 0 {
		if err = s.applySelections(ctx, e, append([]dto.ConfigSelection(nil), binding.ConfigSelection...), false); err != nil {
			return dto.PreparedSession{}, agentengine.Error("session_restore_failed")
		}
	}
	if err = s.saveBinding(e); err != nil {
		return dto.PreparedSession{}, err
	}
	e.mu.Lock()
	e.preparing = false
	e.readyState = true
	e.mu.Unlock()
	// Credentials may have changed outside Platform. A successful explicit prepare
	// clears the cached failure, but only a completed prompt verifies authentication.
	s.mu.Lock()
	if auth := s.auth[engineID]; auth != nil && auth.state == "required" && auth.operation == nil {
		auth.state = "unknown"
	}
	s.mu.Unlock()
	prepared, err := s.Inspect(chatID)
	if err != nil {
		return dto.PreparedSession{}, err
	}
	if preview {
		// Keep the worker setup lock through probe cleanup. A real session must
		// not attach before a failed close could invalidate this connection.
		if err := p.close(true); err != nil {
			return dto.PreparedSession{}, err
		}
		e.mu.Lock()
		e.process = nil
		e.readyState = false
		e.mu.Unlock()
	}
	if method == "session/new" && previewKey != "" {
		s.mu.Lock()
		s.cachePreview(previewKey, defaults)
		s.mu.Unlock()
	}
	keep = true
	return prepared, nil
}

// CloseChat releases a live ACP session without deleting its durable binding.
func (s *Sessions) CloseChat(chatID string) error {
	e, err := s.acquire(chatID)
	if err != nil {
		return err
	}
	defer s.release(chatID, e)
	if e.process == nil {
		return nil
	}
	err = e.process.Close()
	e.mu.Lock()
	e.process = nil
	e.prepared = dto.PreparedSession{}
	e.mu.Unlock()
	return err
}

func (s *Sessions) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, a := range s.auth {
		if a.operation != nil {
			a.operation.cancel()
		}
	}
	s.mu.Unlock()
	err := s.host.Close()
	s.operations.Wait()
	s.workerTasks.Wait()
	<-s.reaperDone
	return err
}

func (s *Sessions) ready(e *session, preparation string, revision int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.readyState || e.process == nil || e.prepared.PreparationID != preparation || e.prepared.ConfigRevision != revision {
		return agentengine.Error("session_configuration_stale")
	}
	select {
	case <-e.process.Done():
		return agentengine.Error("session_configuration_stale")
	case <-e.process.retired:
		return agentengine.Error("session_configuration_stale")
	default:
	}
	var engineID, workspace string
	var err error
	if e.childDefinition != nil {
		engineID, workspace, err = s.ValidateTarget(*e.childDefinition)
	} else {
		engineID, workspace, err = s.target(e.binding.ChatID)
	}
	if err != nil {
		return err
	}
	if engineID != e.binding.EngineID || workspace != e.binding.WorkspaceRoot {
		return agentengine.Error("session_restore_failed")
	}
	return nil
}

// Prompt persists uncertainty before sending. Query/Run event mapping is ACP-007;
// this boundary never retries a request that might have reached the external CLI.
func (s *Sessions) Prompt(ctx context.Context, chatID, preparation string, revision int64, content []sdk.ContentBlock, handler Handler) (json.RawMessage, error) {
	e, err := s.acquire(chatID)
	if err != nil {
		return nil, err
	}
	defer s.release(chatID, e)
	if err = s.ready(e, preparation, revision); err != nil {
		return nil, err
	}
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	e.handler = handler
	e.turnCtx = turnCtx
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.handler, e.turnCtx = nil, nil
		e.mu.Unlock()
		cancel()
		e.turnCalls.Wait()
	}()
	e.mu.Lock()
	e.binding.ConfigSelection = confirmedSelections(e.prepared.ConfigOptions)
	e.mu.Unlock()
	beforeState := e.binding.ContextState
	e.binding.ContextState = "submitted"
	if err = s.saveBinding(e); err != nil {
		return nil, err
	}
	result, err := e.process.Request(ctx, "session/prompt", map[string]any{"sessionId": e.binding.ACPSessionID, "prompt": content})
	e.mu.Lock()
	pending := e.pendingInteractions > 0
	e.mu.Unlock()
	if err == nil && pending && ctx.Err() == nil {
		e.process.Close()
		err = &CallError{ExecutionState: "unknown", Cause: agentengine.Error("rpc_connection_lost")}
	}

	var rpc *sdk.RequestError
	if errors.As(err, &rpc) && rpc.Code == -32000 {
		s.observeAuth(e.binding.EngineID, nil, "required")
		e.process.worker.invalidate()
		e.process.Close()
		e.mu.Lock()
		e.process = nil
		e.readyState = false
		e.binding.ContextState = beforeState
		e.mu.Unlock()
		if saveErr := s.saveBinding(e); saveErr != nil {
			return nil, saveErr
		}
	}

	if err != nil {
		var call *CallError
		if errors.As(err, &call) && call.ExecutionState == "not_started" {
			e.binding.ContextState = beforeState
			if saveErr := s.saveBinding(e); saveErr != nil {
				return nil, &CallError{ExecutionState: "not_started", Cause: saveErr}
			}
		}
		return nil, err
	}
	var response sdk.PromptResponse
	valid := json.Unmarshal(result, &response) == nil
	switch response.StopReason {
	case sdk.StopReasonEndTurn, sdk.StopReasonMaxTokens, sdk.StopReasonMaxTurnRequests, sdk.StopReasonRefusal, sdk.StopReasonCancelled:
	default:
		valid = false
	}
	if !valid {
		e.process.Close()
		return nil, &CallError{ExecutionState: "unknown", Cause: agentengine.Error("rpc_connection_lost")}
	}
	e.mu.Lock()
	e.binding.ConfigSelection = confirmedSelections(e.prepared.ConfigOptions)
	e.mu.Unlock()
	// session/new may succeed before the upstream checks credentials. A cancelled
	// prompt can also return before a model call, so neither proves authentication.
	if response.StopReason != sdk.StopReasonCancelled {
		s.observeAuth(e.binding.EngineID, nil, "authenticated")
	}
	e.binding.ContextState = "persisted"
	if err = s.saveBinding(e); err != nil {
		e.process.Close()
		return nil, &CallError{ExecutionState: "unknown", Cause: err}
	}
	return result, nil
}

func (s *Sessions) Details(chatID string) (SessionDetails, error) {
	if _, err := s.Inspect(chatID); err != nil {
		return SessionDetails{}, err
	}
	s.mu.Lock()
	e := s.entries[chatID]
	s.mu.Unlock()
	if e == nil {
		return SessionDetails{}, ErrClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	data, _ := json.Marshal(e.details)
	var result SessionDetails
	json.Unmarshal(data, &result)
	return result, nil
}
func (s *Sessions) incoming(ctx context.Context, e *session, in Incoming) (any, *sdk.RequestError) {
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Type              string            `json:"sessionUpdate"`
			ToolCallID        string            `json:"toolCallId"`
			Kind              string            `json:"kind"`
			Status            string            `json:"status"`
			ConfigOptions     []json.RawMessage `json:"configOptions"`
			AvailableCommands []json.RawMessage `json:"availableCommands"`
			CurrentModeID     string            `json:"currentModeId"`
		} `json:"update"`
	}
	if json.Unmarshal(in.Params, &params) != nil {
		return nil, sdk.NewInvalidParams(nil)
	}
	e.mu.Lock()
	if in.Generation != e.generation {
		e.mu.Unlock()
		return nil, sdk.NewRequestCancelled(nil)
	}
	if params.SessionID == "" || (e.sessionID != "" && params.SessionID != e.sessionID) || (e.observedSessionID != "" && params.SessionID != e.observedSessionID) {
		e.mu.Unlock()
		return nil, sdk.NewInvalidParams(nil)
	}
	e.observedSessionID = params.SessionID
	if in.Method == "terminal/create" {
		e.backgroundPossible = true
	}
	if in.Method == "session/update" && (params.Update.Type == "tool_call" || params.Update.Type == "tool_call_update") {
		e.trackTool(params.Update.ToolCallID, params.Update.Kind, params.Update.Status, params.Update.Type == "tool_call")
	}
	if in.Method == "session/update" && len(in.ID) == 0 {
		switch params.Update.Type {
		case "config_option_update":
			options, err := configOptions(params.Update.ConfigOptions)
			if err != nil {
				p := e.process
				e.mu.Unlock()
				p.stop()
				return nil, sdk.NewInvalidParams(nil)
			}
			e.prepared.ConfigOptions = options
			e.prepared.ConfigRevision++
			e.configNotified = true
		case "available_commands_update":
			if len(params.Update.AvailableCommands) > 256 {
				p := e.process
				e.mu.Unlock()
				p.stop()
				return nil, sdk.NewInvalidParams(nil)
			}
			e.details.AvailableCommands = params.Update.AvailableCommands
		case "current_mode_update":
			e.details.CurrentModeID = params.Update.CurrentModeID
		}
	}
	handler, turnCtx, preparing := e.handler, e.turnCtx, e.preparing
	if !preparing && handler != nil && turnCtx != nil {
		e.turnCalls.Add(1)
		if len(in.ID) > 0 {
			e.pendingInteractions++
		}
		defer func() {
			e.mu.Lock()
			if len(in.ID) > 0 {
				e.pendingInteractions--
			}
			e.mu.Unlock()
			e.turnCalls.Done()
		}()
	}
	e.mu.Unlock()
	// Restoration may emit the full old transcript. Only current state above is
	// retained; historical content is never passed to a new Run's event handler.
	if preparing || handler == nil || turnCtx == nil {
		if len(in.ID) > 0 {
			return nil, sdk.NewMethodNotFound(in.Method)
		}
		return nil, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(turnCtx, cancel)
	defer stop()
	if turnCtx.Err() != nil {
		return nil, sdk.NewRequestCancelled(nil)
	}
	return handler(ctx, in)
}

func controlRequest(ctx context.Context, p interface {
	Request(context.Context, string, any) (json.RawMessage, error)
}, method string, params any) (json.RawMessage, error) {
	result, err := p.Request(ctx, method, params)
	var rpc *sdk.RequestError
	if errors.As(err, &rpc) {
		if rpc.Code == -32000 {
			return nil, agentengine.Error("auth_required")
		}
		// Preserve only this stable dependency error, never arbitrary Adapter text.
		if rpc.Code == -32603 && rpc.Message == "dependency_missing" {
			return nil, agentengine.Error("dependency_missing")
		}
	}
	return result, err
}
