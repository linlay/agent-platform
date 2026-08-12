package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	mathrand "math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/ws"
)

const (
	agentRegisterType           = "agent.register"
	agentUnregisterType         = "agent.unregister"
	agentListType               = "agent.list"
	agentConnectedPushType      = "connected"
	agentRegistrationVersion    = "1"
	agentRegistrationModeSingle = "SINGLE_EMPLOYEE"
	agentRegistrationModeMulti  = "MULTI_EMPLOYEE"
	agentCardStatusPending      = "pending"
	agentCardStatusAccepted     = "accepted"
	agentCardStatusRejected     = "rejected"
	agentCardStatusRetrying     = "retrying"
	agentCardStatusError        = "error"
	agentCardStatusOffline      = "offline"
	defaultCardDebounce         = 2 * time.Second
	defaultCardAckTimeout       = 10 * time.Second
	defaultCardMaxConcurrent    = 4
	defaultCardMaxBytes         = 256 << 10
	defaultCardNameRunes        = 256
	defaultCardDescriptionRunes = 2048
)

var stableAgentCapabilities = []string{"query", "steer", "interrupt", "hitl"}

var (
	agentRegistrationCredentialPattern   = regexp.MustCompile(`(?i)(?:bearer\s+[a-z0-9._~+/=-]{8,}|(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|token|cookie)\s*[:=]\s*\S+)`)
	agentRegistrationAbsolutePathPattern = regexp.MustCompile(`(?i)(?:^|[\s"'(])(?:/(?:users|home|root|private|var|etc|opt|srv|tmp|mnt|volumes|workspace)/\S+|[a-z]:\\\S+)`)
)

type agentCardCatalog interface {
	Agents(scope string) []api.AgentSummary
	AgentDefinition(key string) (catalog.AgentDefinition, bool)
}

type agentCardReporterOptions struct {
	Debounce      time.Duration
	AckTimeout    time.Duration
	RetryDelays   []time.Duration
	MaxConcurrent int
	MaxCardBytes  int
}

type AgentCardReporter struct {
	ctx     context.Context
	catalog agentCardCatalog
	options agentCardReporterOptions

	mu       sync.Mutex
	sessions map[*ws.Conn]*agentCardConnection
	timer    *time.Timer
	rng      *mathrand.Rand
}

type agentCardConnection struct {
	channelID      string
	conn           *ws.Conn
	ctx            context.Context
	cancel         context.CancelFunc
	handshakeTimer *time.Timer
	cycleCancel    context.CancelFunc
	generation     uint64
	connected      *api.GatewayAgentConnectedData
	conflicted     bool
	statuses       map[string]api.GatewayAgentCardReportStatus
	registrations  map[string]string
}

type builtAgentRegistration struct {
	agentKey string
	payload  api.GatewayAgentRegistration
	expanded []string
}

type cardBuildFailure struct {
	agentKey string
	err      error
}

type gatewayRequestOutcome struct {
	data      json.RawMessage
	retryable bool
	rejected  bool
	canceled  bool
	reason    string
	requestID string
	attempt   int
}

func NewAgentCardReporter(ctx context.Context, source agentCardCatalog) *AgentCardReporter {
	return newAgentCardReporter(ctx, source, agentCardReporterOptions{})
}

func newAgentCardReporter(ctx context.Context, source agentCardCatalog, options agentCardReporterOptions) *AgentCardReporter {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Debounce <= 0 {
		options.Debounce = defaultCardDebounce
	}
	if options.AckTimeout <= 0 {
		options.AckTimeout = defaultCardAckTimeout
	}
	if len(options.RetryDelays) == 0 {
		options.RetryDelays = []time.Duration{2 * time.Second, 4 * time.Second}
	} else {
		options.RetryDelays = append([]time.Duration(nil), options.RetryDelays...)
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = defaultCardMaxConcurrent
	}
	if options.MaxCardBytes <= 0 {
		options.MaxCardBytes = defaultCardMaxBytes
	}
	return &AgentCardReporter{
		ctx:      ctx,
		catalog:  source,
		options:  options,
		sessions: map[*ws.Conn]*agentCardConnection{},
		rng:      mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
	}
}

func (r *AgentCardReporter) CatalogReloaded(_ context.Context, _ string) {
	r.ScheduleRefresh()
}

func (r *AgentCardReporter) ScheduleRefresh() {
	if r == nil || r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.options.Debounce, r.refreshAll)
	r.mu.Unlock()
}

func (r *AgentCardReporter) ChannelConnected(channelID string, conn *ws.Conn, handshakeTimeout time.Duration) {
	if r == nil || conn == nil || r.ctx.Err() != nil {
		return
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return
	}
	if handshakeTimeout <= 0 {
		handshakeTimeout = 10 * time.Second
	}
	connectionCtx, cancel := context.WithCancel(r.ctx)
	session := &agentCardConnection{
		channelID:     channelID,
		conn:          conn,
		ctx:           connectionCtx,
		cancel:        cancel,
		statuses:      map[string]api.GatewayAgentCardReportStatus{},
		registrations: map[string]string{},
	}
	r.mu.Lock()
	if previous := r.sessions[conn]; previous != nil {
		previous.cancel()
		if previous.handshakeTimer != nil {
			previous.handshakeTimer.Stop()
		}
	}
	r.sessions[conn] = session
	r.mu.Unlock()
	session.handshakeTimer = time.AfterFunc(handshakeTimeout, func() {
		r.markHandshakeTimeout(session)
	})
}

func (r *AgentCardReporter) ChannelPush(channelID string, conn *ws.Conn, push ws.PushFrame) {
	if r == nil || conn == nil || strings.TrimSpace(push.Type) != agentConnectedPushType {
		return
	}
	var connected api.GatewayAgentConnectedData
	raw, err := json.Marshal(push.Data)
	if err == nil {
		err = json.Unmarshal(raw, &connected)
	}
	if err != nil {
		r.failHandshake(conn, "invalid connected payload")
		return
	}
	connected.SessionID = strings.TrimSpace(connected.SessionID)
	connected.PlatformKey = strings.TrimSpace(connected.PlatformKey)
	connected.RegistrationMode = strings.ToUpper(strings.TrimSpace(connected.RegistrationMode))
	connected.AgentRegistration.Version = strings.TrimSpace(connected.AgentRegistration.Version)
	if err := validateConnectedData(connected); err != nil {
		r.failHandshake(conn, err.Error())
		return
	}

	r.mu.Lock()
	session := r.sessions[conn]
	if session == nil || session.channelID != strings.TrimSpace(channelID) || session.ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	if session.conflicted {
		r.mu.Unlock()
		return
	}
	if session.connected != nil {
		previous := session.connected
		if previous.PlatformKey != connected.PlatformKey || previous.RegistrationMode != connected.RegistrationMode || previous.AgentRegistration.Version != connected.AgentRegistration.Version {
			session.conflicted = true
			if session.cycleCancel != nil {
				session.cycleCancel()
			}
			r.setAllSessionStatusesLocked(session, agentCardStatusError, "conflicting connected payload")
			r.mu.Unlock()
			return
		}
		if connectedDataEqual(*previous, connected) {
			r.mu.Unlock()
			return
		}
	}
	connected.AgentRegistration.SupportedCapabilities = normalizedCapabilities(connected.AgentRegistration.SupportedCapabilities)
	session.connected = &connected
	session.conflicted = false
	if session.handshakeTimer != nil {
		session.handshakeTimer.Stop()
		session.handshakeTimer = nil
	}
	r.mu.Unlock()
	r.startCycle(session)
}

func (r *AgentCardReporter) ChannelDisconnected(channelID string, conn *ws.Conn) {
	if r == nil || conn == nil {
		return
	}
	r.mu.Lock()
	session := r.sessions[conn]
	if session == nil || session.channelID != strings.TrimSpace(channelID) {
		r.mu.Unlock()
		return
	}
	if session.handshakeTimer != nil {
		session.handshakeTimer.Stop()
	}
	session.cancel()
	delete(r.sessions, conn)
	r.mu.Unlock()
}

func (r *AgentCardReporter) markHandshakeTimeout(session *agentCardConnection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[session.conn] != session || session.connected != nil || session.ctx.Err() != nil {
		return
	}
	r.setAllSessionStatusesLocked(session, agentCardStatusError, "gateway connected capability declaration timed out")
}

func (r *AgentCardReporter) failHandshake(conn *ws.Conn, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[conn]
	if session == nil || session.ctx.Err() != nil {
		return
	}
	if session.cycleCancel != nil {
		session.cycleCancel()
	}
	session.conflicted = true
	r.setAllSessionStatusesLocked(session, agentCardStatusError, reason)
}

func validateConnectedData(data api.GatewayAgentConnectedData) error {
	if strings.TrimSpace(data.SessionID) == "" {
		return fmt.Errorf("connected.sessionId is required")
	}
	if strings.TrimSpace(data.PlatformKey) == "" {
		return fmt.Errorf("connected.platformKey is required")
	}
	if strings.TrimSpace(data.AgentRegistration.Version) != agentRegistrationVersion {
		return fmt.Errorf("gateway does not support agent registration v1")
	}
	mode := strings.ToUpper(strings.TrimSpace(data.RegistrationMode))
	if mode != agentRegistrationModeSingle && mode != agentRegistrationModeMulti {
		return fmt.Errorf("connected.registrationMode is invalid")
	}
	data.RegistrationMode = mode
	if data.AgentRegistration.MaxAgentsPerPlatformChannel <= 0 {
		return fmt.Errorf("connected.agentRegistration.maxAgentsPerPlatformChannel must be positive")
	}
	seen := map[string]struct{}{}
	for _, capability := range data.AgentRegistration.SupportedCapabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			return fmt.Errorf("connected supportedCapabilities contains an empty capability")
		}
		if _, exists := seen[capability]; exists {
			return fmt.Errorf("connected supportedCapabilities contains duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func connectedDataEqual(left, right api.GatewayAgentConnectedData) bool {
	left.AgentRegistration.SupportedCapabilities = normalizedCapabilities(left.AgentRegistration.SupportedCapabilities)
	right.AgentRegistration.SupportedCapabilities = normalizedCapabilities(right.AgentRegistration.SupportedCapabilities)
	return left.SessionID == right.SessionID && left.PlatformKey == right.PlatformKey && left.RegistrationMode == right.RegistrationMode &&
		left.AgentRegistration.Version == right.AgentRegistration.Version &&
		left.AgentRegistration.MaxAgentsPerPlatformChannel == right.AgentRegistration.MaxAgentsPerPlatformChannel &&
		stringSlicesEqual(left.AgentRegistration.SupportedCapabilities, right.AgentRegistration.SupportedCapabilities)
}

func (r *AgentCardReporter) refreshAll() {
	if r == nil || r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.timer = nil
	sessions := make([]*agentCardConnection, 0, len(r.sessions))
	for _, session := range r.sessions {
		if session.connected != nil && !session.conflicted {
			sessions = append(sessions, session)
		}
	}
	r.mu.Unlock()
	for _, session := range sessions {
		r.startCycle(session)
	}
}

func (r *AgentCardReporter) startCycle(session *agentCardConnection) {
	if r == nil || session == nil || session.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	if r.sessions[session.conn] != session || session.connected == nil || session.conflicted {
		r.mu.Unlock()
		return
	}
	if session.cycleCancel != nil {
		session.cycleCancel()
	}
	cycleCtx, cancel := context.WithCancel(session.ctx)
	session.cycleCancel = cancel
	session.generation++
	generation := session.generation
	connected := *session.connected
	connected.AgentRegistration.SupportedCapabilities = append([]string(nil), session.connected.AgentRegistration.SupportedCapabilities...)
	r.mu.Unlock()
	go r.reconcile(cycleCtx, session, generation, connected)
}

func (r *AgentCardReporter) reconcile(ctx context.Context, session *agentCardConnection, generation uint64, connected api.GatewayAgentConnectedData) {
	desired, failures := r.buildRegistrations(session.channelID, connected.AgentRegistration.SupportedCapabilities)
	r.setBuildFailures(session, generation, failures)
	if connected.RegistrationMode == agentRegistrationModeSingle && len(desired) > 1 {
		reason := "SINGLE_EMPLOYEE registration mode allows only one exported agent"
		for _, item := range desired {
			r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: time.Now().UnixMilli(), Reason: reason})
		}
		return
	}
	if len(desired) > connected.AgentRegistration.MaxAgentsPerPlatformChannel {
		reason := fmt.Sprintf("exported agent count exceeds gateway limit %d", connected.AgentRegistration.MaxAgentsPerPlatformChannel)
		for _, item := range desired {
			r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: time.Now().UnixMilli(), Reason: reason})
		}
		return
	}

	initial, outcome := r.listAgents(ctx, session, func(outcome gatewayRequestOutcome) {
		r.setAllDesiredOutcome(session, generation, desired, outcome, "initial agent.list retrying")
	})
	if outcome.canceled {
		return
	}
	if outcome.reason != "" {
		r.setAllDesiredOutcome(session, generation, desired, outcome, "initial agent.list failed")
		return
	}
	if initial.PlatformKey != connected.PlatformKey {
		r.setAllDesiredStatus(session, generation, desired, agentCardStatusError, "agent.list platformKey mismatch")
		return
	}

	desiredByKey := make(map[string]builtAgentRegistration, len(desired))
	for _, item := range desired {
		desiredByKey[item.agentKey] = item
		r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusPending, UpdatedAt: time.Now().UnixMilli()})
	}
	owned := ownedAgents(initial.Agents)
	unregisterKeys := make([]string, 0)
	for key := range owned {
		if _, exists := desiredByKey[key]; !exists {
			unregisterKeys = append(unregisterKeys, key)
		}
	}
	sort.Strings(unregisterKeys)
	r.runConcurrent(ctx, session, unregisterKeys, r.maxConcurrent(connected), func(agentKey string) {
		outcome := r.unregisterAgent(ctx, session, agentKey)
		if outcome.reason != "" && !outcome.canceled {
			log.Printf("[agent-registration] unregister failed: channel=%s agent=%s err=%s", session.channelID, sanitizeCardReason(agentKey), sanitizeCardReason(outcome.reason))
		}
	})
	if ctx.Err() != nil {
		return
	}

	registerItems := make([]builtAgentRegistration, 0)
	for _, item := range desired {
		remote, exists := owned[item.agentKey]
		if !exists || !registrationMatches(item, remote) {
			registerItems = append(registerItems, item)
		}
	}
	r.runConcurrentRegistrations(ctx, session, registerItems, r.maxConcurrent(connected), func(item builtAgentRegistration) {
		outcome, result := r.registerAgent(ctx, session, item, func(outcome gatewayRequestOutcome) {
			r.setOutcomeStatus(session, generation, item.agentKey, outcome, "agent.register retrying")
		})
		if outcome.canceled {
			return
		}
		if outcome.reason != "" {
			r.setOutcomeStatus(session, generation, item.agentKey, outcome, "agent.register failed")
			return
		}
		r.mu.Lock()
		if r.sessions[session.conn] == session && session.generation == generation {
			session.registrations[item.agentKey] = strings.TrimSpace(result.RegistrationID)
		}
		r.mu.Unlock()
	})
	if ctx.Err() != nil {
		return
	}

	finalList, outcome := r.listAgents(ctx, session, func(outcome gatewayRequestOutcome) {
		r.setAllDesiredOutcome(session, generation, desired, outcome, "final agent.list retrying")
	})
	if outcome.canceled {
		return
	}
	if outcome.reason != "" {
		r.setAllDesiredOutcome(session, generation, desired, outcome, "final agent.list failed")
		return
	}
	if finalList.PlatformKey != connected.PlatformKey {
		r.setAllDesiredStatus(session, generation, desired, agentCardStatusError, "final agent.list platformKey mismatch")
		return
	}
	r.verifyFinalList(session, generation, desired, failures, finalList.Agents)
}

func (r *AgentCardReporter) maxConcurrent(connected api.GatewayAgentConnectedData) int {
	if connected.RegistrationMode == agentRegistrationModeSingle {
		return 1
	}
	return r.options.MaxConcurrent
}

func (r *AgentCardReporter) runConcurrent(ctx context.Context, session *agentCardConnection, keys []string, limit int, fn func(string)) {
	if limit <= 0 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, key := range keys {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			fn(key)
		}()
	}
	wg.Wait()
}

func (r *AgentCardReporter) runConcurrentRegistrations(ctx context.Context, session *agentCardConnection, items []builtAgentRegistration, limit int, fn func(builtAgentRegistration)) {
	if limit <= 0 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, item := range items {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			fn(item)
		}()
	}
	wg.Wait()
}

func (r *AgentCardReporter) listAgents(ctx context.Context, session *agentCardConnection, onRetry func(gatewayRequestOutcome)) (api.GatewayAgentListResult, gatewayRequestOutcome) {
	outcome := r.sendWithRetry(ctx, session.conn, agentListType, map[string]any{}, onRetry)
	if outcome.reason != "" || outcome.canceled {
		return api.GatewayAgentListResult{}, outcome
	}
	var result api.GatewayAgentListResult
	if err := json.Unmarshal(outcome.data, &result); err != nil {
		outcome.reason = "agent.list response data is invalid"
		return api.GatewayAgentListResult{}, outcome
	}
	if result.Count != len(result.Agents) {
		outcome.reason = "agent.list count does not match agents"
		return api.GatewayAgentListResult{}, outcome
	}
	ownedCount := 0
	seen := map[string]struct{}{}
	for _, agent := range result.Agents {
		key := strings.TrimSpace(agent.AgentKey)
		if key == "" {
			outcome.reason = "agent.list contains an empty agentKey"
			return api.GatewayAgentListResult{}, outcome
		}
		if _, duplicate := seen[key]; duplicate {
			outcome.reason = "agent.list contains duplicate agentKey"
			return api.GatewayAgentListResult{}, outcome
		}
		seen[key] = struct{}{}
		if agent.OwnedByCurrentSession {
			ownedCount++
		}
	}
	if result.CurrentSessionOwnedCount != ownedCount {
		outcome.reason = "agent.list currentSessionOwnedCount mismatch"
		return api.GatewayAgentListResult{}, outcome
	}
	return result, outcome
}

func (r *AgentCardReporter) registerAgent(ctx context.Context, session *agentCardConnection, item builtAgentRegistration, onRetry func(gatewayRequestOutcome)) (gatewayRequestOutcome, api.GatewayAgentRegisterResult) {
	outcome := r.sendWithRetry(ctx, session.conn, agentRegisterType, item.payload, onRetry)
	if outcome.reason != "" || outcome.canceled {
		return outcome, api.GatewayAgentRegisterResult{}
	}
	var result api.GatewayAgentRegisterResult
	if err := json.Unmarshal(outcome.data, &result); err != nil || result.Accepted == nil {
		outcome.reason = "agent.register response data is invalid"
		return outcome, api.GatewayAgentRegisterResult{}
	}
	if strings.TrimSpace(result.AgentKey) != item.agentKey {
		outcome.reason = "agent.register response agentKey mismatch"
		return outcome, api.GatewayAgentRegisterResult{}
	}
	if !*result.Accepted {
		outcome.rejected = true
		outcome.reason = firstCardReason(result.Message, result.ErrorCode, "gateway rejected agent registration")
		return outcome, result
	}
	status := strings.ToUpper(strings.TrimSpace(result.Status))
	if (status != "REGISTERED" && status != "UPDATED") || strings.TrimSpace(result.RegistrationID) == "" {
		outcome.reason = "agent.register success response is incomplete"
		return outcome, api.GatewayAgentRegisterResult{}
	}
	if !capabilitySetsEqual(result.Capabilities, item.expanded) {
		outcome.reason = "agent.register response capabilities mismatch"
		return outcome, api.GatewayAgentRegisterResult{}
	}
	return outcome, result
}

func (r *AgentCardReporter) unregisterAgent(ctx context.Context, session *agentCardConnection, agentKey string) gatewayRequestOutcome {
	outcome := r.sendWithRetry(ctx, session.conn, agentUnregisterType, api.GatewayAgentUnregisterPayload{AgentKey: agentKey}, nil)
	if outcome.reason != "" || outcome.canceled {
		return outcome
	}
	var result api.GatewayAgentUnregisterResult
	if err := json.Unmarshal(outcome.data, &result); err != nil {
		outcome.reason = "agent.unregister response data is invalid"
		return outcome
	}
	if strings.TrimSpace(result.AgentKey) != agentKey {
		outcome.reason = "agent.unregister response agentKey mismatch"
		return outcome
	}
	status := strings.ToUpper(strings.TrimSpace(result.Status))
	if status != "UNREGISTERED" && status != "NOT_REGISTERED" {
		outcome.reason = firstCardReason(result.Message, result.ErrorCode, "agent.unregister response status is invalid")
		return outcome
	}
	if result.Accepted == nil || !*result.Accepted {
		outcome.reason = firstCardReason(result.Message, result.ErrorCode, "gateway did not accept agent unregistration")
		return outcome
	}
	r.mu.Lock()
	delete(session.registrations, agentKey)
	r.mu.Unlock()
	return outcome
}

func (r *AgentCardReporter) sendWithRetry(ctx context.Context, conn *ws.Conn, frameType string, payload any, onRetry func(gatewayRequestOutcome)) gatewayRequestOutcome {
	maxAttempts := len(r.options.RetryDelays) + 1
	var last gatewayRequestOutcome
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return gatewayRequestOutcome{canceled: true}
		}
		requestID := newAgentCardRequestID()
		last = r.sendOnce(ctx, conn, frameType, requestID, payload)
		last.requestID = requestID
		last.attempt = attempt
		if last.canceled || !last.retryable {
			return last
		}
		if attempt == maxAttempts {
			// There is no later attempt that can still be in progress. Preserve
			// the reason but expose a terminal error instead of retrying forever.
			last.retryable = false
			return last
		}
		if onRetry != nil {
			onRetry(last)
		}
		timer := time.NewTimer(r.jitter(r.options.RetryDelays[attempt-1]))
		select {
		case <-ctx.Done():
			timer.Stop()
			return gatewayRequestOutcome{canceled: true}
		case <-timer.C:
		}
	}
	return last
}

func (r *AgentCardReporter) sendOnce(ctx context.Context, conn *ws.Conn, frameType, requestID string, payload any) gatewayRequestOutcome {
	raw, err := json.Marshal(payload)
	if err != nil {
		return gatewayRequestOutcome{reason: err.Error()}
	}
	if len(raw) > r.options.MaxCardBytes {
		return gatewayRequestOutcome{reason: fmt.Sprintf("%s request exceeds %d bytes", frameType, r.options.MaxCardBytes)}
	}
	frames, cleanup, err := conn.OpenOutboundRequest(ws.RequestFrame{Frame: ws.FrameRequest, Type: frameType, ID: requestID, Payload: raw})
	if err != nil {
		return gatewayRequestOutcome{retryable: true, reason: err.Error()}
	}
	defer cleanup()
	timer := time.NewTimer(r.options.AckTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return gatewayRequestOutcome{canceled: true}
	case <-conn.Done():
		return gatewayRequestOutcome{canceled: true}
	case <-timer.C:
		return gatewayRequestOutcome{retryable: true, reason: frameType + " response timed out"}
	case data, ok := <-frames:
		if !ok {
			return gatewayRequestOutcome{retryable: true, reason: "gateway connection closed before response"}
		}
		return decodeGatewayResponse(data, requestID, frameType)
	}
}

func decodeGatewayResponse(data []byte, requestID, frameType string) gatewayRequestOutcome {
	var frame struct {
		Frame string          `json:"frame"`
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Code  int             `json:"code"`
		Msg   string          `json:"msg"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return gatewayRequestOutcome{reason: "invalid gateway response"}
	}
	if strings.TrimSpace(frame.ID) != requestID {
		return gatewayRequestOutcome{reason: "gateway response id mismatch"}
	}
	switch strings.ToLower(strings.TrimSpace(frame.Frame)) {
	case ws.FrameError:
		return gatewayRequestOutcome{retryable: frame.Code >= 500, rejected: frame.Code > 0 && frame.Code < 500, reason: firstCardReason(frame.Msg, "gateway rejected request")}
	case ws.FrameResponse:
		if strings.TrimSpace(frame.Type) != frameType {
			return gatewayRequestOutcome{reason: "gateway response type mismatch"}
		}
		if frame.Code != 0 {
			return gatewayRequestOutcome{retryable: frame.Code >= 500, rejected: frame.Code > 0 && frame.Code < 500, reason: firstCardReason(frame.Msg, "gateway returned a non-zero response code")}
		}
		if len(frame.Data) == 0 || string(frame.Data) == "null" {
			return gatewayRequestOutcome{reason: "gateway response data is required"}
		}
		return gatewayRequestOutcome{data: frame.Data}
	default:
		return gatewayRequestOutcome{reason: "unexpected gateway response frame"}
	}
}

func (r *AgentCardReporter) buildRegistrations(channelID string, supported []string) ([]builtAgentRegistration, []cardBuildFailure) {
	if r == nil || r.catalog == nil {
		return nil, nil
	}
	supportedSet := capabilitySet(supported)
	exactStableSupport := len(supportedSet) == len(stableAgentCapabilities)
	for _, capability := range stableAgentCapabilities {
		if _, exists := supportedSet[capability]; !exists {
			exactStableSupport = false
		}
	}
	registrationsByKey := map[string]builtAgentRegistration{}
	failuresByKey := map[string]cardBuildFailure{}
	for _, summary := range r.catalog.Agents("all") {
		def, ok := r.catalog.AgentDefinition(summary.Key)
		if !ok || catalog.AgentIsChannelMode(def.Mode) {
			continue
		}
		for _, export := range def.ChannelConfig.Exports {
			if strings.TrimSpace(export.ChannelID) != strings.TrimSpace(channelID) || !export.Allow.Query {
				continue
			}
			externalKey := catalog.EffectiveChannelExportExternalKey(def.Key, export)
			if _, duplicate := registrationsByKey[externalKey]; duplicate {
				delete(registrationsByKey, externalKey)
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: fmt.Errorf("duplicate external agent key on channel")}
				continue
			}
			if _, duplicate := failuresByKey[externalKey]; duplicate {
				continue
			}
			payload, expanded, err := r.buildRegistration(def, externalKey, export, supportedSet, exactStableSupport)
			if err != nil {
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: err}
				continue
			}
			registrationsByKey[externalKey] = builtAgentRegistration{agentKey: externalKey, payload: payload, expanded: expanded}
		}
	}
	registrations := make([]builtAgentRegistration, 0, len(registrationsByKey))
	for _, item := range registrationsByKey {
		registrations = append(registrations, item)
	}
	sort.Slice(registrations, func(i, j int) bool { return registrations[i].agentKey < registrations[j].agentKey })
	failures := make([]cardBuildFailure, 0, len(failuresByKey))
	for _, failure := range failuresByKey {
		failures = append(failures, failure)
	}
	sort.Slice(failures, func(i, j int) bool { return failures[i].agentKey < failures[j].agentKey })
	return registrations, failures
}

func (r *AgentCardReporter) buildRegistration(def catalog.AgentDefinition, externalKey string, export catalog.AgentChannelExport, supported map[string]struct{}, exactStableSupport bool) (api.GatewayAgentRegistration, []string, error) {
	payload := api.GatewayAgentRegistration{
		AgentKey:    strings.TrimSpace(externalKey),
		Name:        firstCardReason(def.Name, def.Key),
		Role:        strings.TrimSpace(def.Role),
		Description: strings.TrimSpace(def.Description),
	}
	if err := validateCardText("agentKey", payload.AgentKey, defaultCardNameRunes, true); err != nil {
		return api.GatewayAgentRegistration{}, nil, err
	}
	if err := validateCardText("name", payload.Name, defaultCardNameRunes, true); err != nil {
		return api.GatewayAgentRegistration{}, nil, err
	}
	if err := validateCardText("role", payload.Role, defaultCardNameRunes, false); err != nil {
		return api.GatewayAgentRegistration{}, nil, err
	}
	if err := validateCardText("description", payload.Description, defaultCardDescriptionRunes, false); err != nil {
		return api.GatewayAgentRegistration{}, nil, err
	}
	expanded := []string{"query"}
	if export.Allow.Steer {
		expanded = append(expanded, "steer")
	}
	if export.Allow.Interrupt {
		expanded = append(expanded, "interrupt")
	}
	if export.Allow.Submit {
		expanded = append(expanded, "hitl")
	}
	for _, capability := range expanded {
		if _, exists := supported[capability]; !exists {
			return api.GatewayAgentRegistration{}, nil, fmt.Errorf("gateway does not support required capability %q", capability)
		}
	}
	payload.Capabilities = append([]string(nil), expanded...)
	if len(expanded) == len(stableAgentCapabilities) && exactStableSupport {
		payload.Capabilities = []string{"all"}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return api.GatewayAgentRegistration{}, nil, err
	}
	if len(raw) > r.options.MaxCardBytes {
		return api.GatewayAgentRegistration{}, nil, fmt.Errorf("agent registration exceeds %d bytes", r.options.MaxCardBytes)
	}
	return payload, expanded, nil
}

func validateCardText(field, value string, maxRunes int, required bool) error {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	for _, r := range value {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains invalid control characters", field)
		}
	}
	return nil
}

func registrationMatches(local builtAgentRegistration, remote api.GatewayRegisteredAgent) bool {
	return local.agentKey == strings.TrimSpace(remote.AgentKey) && local.payload.Name == strings.TrimSpace(remote.Name) &&
		local.payload.Role == strings.TrimSpace(remote.Role) && local.payload.Description == strings.TrimSpace(remote.Description) &&
		capabilitySetsEqual(local.expanded, remote.Capabilities)
}

func ownedAgents(agents []api.GatewayRegisteredAgent) map[string]api.GatewayRegisteredAgent {
	result := map[string]api.GatewayRegisteredAgent{}
	for _, agent := range agents {
		if agent.OwnedByCurrentSession {
			result[strings.TrimSpace(agent.AgentKey)] = agent
		}
	}
	return result
}

func (r *AgentCardReporter) verifyFinalList(session *agentCardConnection, generation uint64, desired []builtAgentRegistration, failures []cardBuildFailure, agents []api.GatewayRegisteredAgent) {
	all := map[string]api.GatewayRegisteredAgent{}
	for _, agent := range agents {
		all[strings.TrimSpace(agent.AgentKey)] = agent
	}
	failureKeys := map[string]struct{}{}
	for _, failure := range failures {
		failureKeys[failure.agentKey] = struct{}{}
	}
	desiredKeys := make(map[string]struct{}, len(desired))
	for _, item := range desired {
		desiredKeys[item.agentKey] = struct{}{}
	}
	for _, remote := range agents {
		if !remote.OwnedByCurrentSession {
			continue
		}
		if _, wanted := desiredKeys[strings.TrimSpace(remote.AgentKey)]; wanted {
			continue
		}
		r.setAllDesiredStatus(session, generation, desired, agentCardStatusError, "final agent.list contains a stale current-session registration")
		return
	}
	for _, item := range desired {
		if _, failed := failureKeys[item.agentKey]; failed {
			continue
		}
		remote, exists := all[item.agentKey]
		if !exists {
			r.setStatusUnlessRejected(session, generation, item.agentKey, "agent registration is missing from final agent.list")
			continue
		}
		if !remote.OwnedByCurrentSession {
			r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusRejected, UpdatedAt: time.Now().UnixMilli(), Reason: "agentKey is owned by another session"})
			continue
		}
		if !registrationMatches(item, remote) {
			r.setStatusUnlessRejected(session, generation, item.agentKey, "agent registration differs from final agent.list")
			continue
		}
		now := time.Now().UnixMilli()
		r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusAccepted, UpdatedAt: now, AcceptedAt: now})
	}
}

func (r *AgentCardReporter) setStatusUnlessRejected(session *agentCardConnection, generation uint64, agentKey, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[session.conn] != session || session.generation != generation {
		return
	}
	if session.statuses[agentKey].Status == agentCardStatusRejected {
		return
	}
	session.statuses[agentKey] = api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: time.Now().UnixMilli(), Reason: sanitizeCardReason(reason)}
}

func (r *AgentCardReporter) setBuildFailures(session *agentCardConnection, generation uint64, failures []cardBuildFailure) {
	for _, failure := range failures {
		reason := sanitizeCardReason(failure.err.Error())
		r.setStatus(session, generation, failure.agentKey, api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: time.Now().UnixMilli(), Reason: reason})
		log.Printf("[agent-registration] build rejected: channel=%s agent=%s err=%s", session.channelID, sanitizeCardReason(failure.agentKey), reason)
	}
}

func (r *AgentCardReporter) setAllDesiredOutcome(session *agentCardConnection, generation uint64, desired []builtAgentRegistration, outcome gatewayRequestOutcome, prefix string) {
	for _, item := range desired {
		r.setOutcomeStatus(session, generation, item.agentKey, outcome, prefix)
	}
}

func (r *AgentCardReporter) setOutcomeStatus(session *agentCardConnection, generation uint64, agentKey string, outcome gatewayRequestOutcome, prefix string) {
	status := agentCardStatusError
	if outcome.rejected {
		status = agentCardStatusRejected
	} else if outcome.retryable {
		status = agentCardStatusRetrying
	}
	reason := strings.TrimSpace(prefix + ": " + outcome.reason)
	r.setStatus(session, generation, agentKey, api.GatewayAgentCardReportStatus{Status: status, RequestID: outcome.requestID, Attempt: outcome.attempt, UpdatedAt: time.Now().UnixMilli(), Reason: reason})
}

func (r *AgentCardReporter) setAllDesiredStatus(session *agentCardConnection, generation uint64, desired []builtAgentRegistration, status, reason string) {
	for _, item := range desired {
		r.setStatus(session, generation, item.agentKey, api.GatewayAgentCardReportStatus{Status: status, UpdatedAt: time.Now().UnixMilli(), Reason: reason})
	}
}

func (r *AgentCardReporter) setStatus(session *agentCardConnection, generation uint64, agentKey string, status api.GatewayAgentCardReportStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[session.conn] != session || session.generation != generation {
		return
	}
	status.Reason = sanitizeCardReason(status.Reason)
	session.statuses[strings.TrimSpace(agentKey)] = status
}

func (r *AgentCardReporter) setAllSessionStatusesLocked(session *agentCardConnection, status, reason string) {
	items, failures := r.buildRegistrations(session.channelID, stableAgentCapabilities)
	now := time.Now().UnixMilli()
	for _, item := range items {
		session.statuses[item.agentKey] = api.GatewayAgentCardReportStatus{Status: status, UpdatedAt: now, Reason: sanitizeCardReason(reason)}
	}
	for _, failure := range failures {
		session.statuses[failure.agentKey] = api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: now, Reason: sanitizeCardReason(failure.err.Error())}
	}
}

func (r *AgentCardReporter) AgentCardStatus(channelID, externalAgentKey string) (api.GatewayAgentCardReportStatus, bool) {
	if r == nil {
		return api.GatewayAgentCardReportStatus{}, false
	}
	channelID = strings.TrimSpace(channelID)
	externalAgentKey = strings.TrimSpace(externalAgentKey)
	items, failures := r.buildRegistrations(channelID, stableAgentCapabilities)
	known := false
	for _, item := range items {
		if item.agentKey == externalAgentKey {
			known = true
			break
		}
	}
	for _, failure := range failures {
		if failure.agentKey == externalAgentKey {
			return api.GatewayAgentCardReportStatus{Status: agentCardStatusError, UpdatedAt: time.Now().UnixMilli(), Reason: sanitizeCardReason(failure.err.Error())}, true
		}
	}
	if !known {
		return api.GatewayAgentCardReportStatus{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	statuses := make([]api.GatewayAgentCardReportStatus, 0)
	for _, session := range r.sessions {
		if session.channelID != channelID || session.ctx.Err() != nil {
			continue
		}
		status, exists := session.statuses[externalAgentKey]
		if !exists {
			status = api.GatewayAgentCardReportStatus{Status: agentCardStatusPending, UpdatedAt: time.Now().UnixMilli()}
		}
		statuses = append(statuses, status)
	}
	if len(statuses) == 0 {
		return api.GatewayAgentCardReportStatus{Status: agentCardStatusOffline, UpdatedAt: time.Now().UnixMilli()}, true
	}
	return aggregateCardStatuses(statuses), true
}

func aggregateCardStatuses(statuses []api.GatewayAgentCardReportStatus) api.GatewayAgentCardReportStatus {
	priority := map[string]int{agentCardStatusError: 0, agentCardStatusRejected: 1, agentCardStatusRetrying: 2, agentCardStatusPending: 3, agentCardStatusAccepted: 4, agentCardStatusOffline: 5}
	result := statuses[0]
	for _, status := range statuses[1:] {
		if priority[status.Status] < priority[result.Status] || (priority[status.Status] == priority[result.Status] && status.UpdatedAt > result.UpdatedAt) {
			result = status
		}
	}
	return result
}

func capabilitySet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func normalizedCapabilities(values []string) []string {
	set := capabilitySet(values)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func capabilitySetsEqual(left, right []string) bool {
	return stringSlicesEqual(normalizedCapabilities(left), normalizedCapabilities(right))
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (r *AgentCardReporter) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	r.mu.Lock()
	factor := 0.8 + r.rng.Float64()*0.4
	r.mu.Unlock()
	return time.Duration(float64(base) * factor)
}

func newAgentCardRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "agent_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("agent_%x", time.Now().UnixNano())
}

func sanitizeCardReason(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = agentRegistrationCredentialPattern.ReplaceAllString(value, "[redacted credential]")
	value = agentRegistrationAbsolutePathPattern.ReplaceAllString(value, " [redacted local path]")
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= 512 {
		return value
	}
	runes := []rune(value)
	return string(runes[:512])
}

func firstCardReason(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
