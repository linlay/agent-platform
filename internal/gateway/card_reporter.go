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
	"unicode/utf8"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"
)

const (
	agentCardUpdateType         = "agent.card.update"
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
	defaultCardMaxFeatures      = 256
	defaultCardMaxTags          = 32
	defaultCardNameRunes        = 256
	defaultCardDescriptionRunes = 2048
	defaultCardTagRunes         = 128
)

var (
	cardCredentialPattern   = regexp.MustCompile(`(?i)(?:bearer\s+[a-z0-9._~+/=-]{8,}|(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|token|cookie)\s*[:=]\s*\S+)`)
	cardAbsolutePathPattern = regexp.MustCompile(`(?i)(?:^|[\s"'(])(?:/(?:users|home|root|private|var|etc|opt|srv|tmp|mnt|volumes|workspace)/\S+|[a-z]:\\\S+)`)
)

type agentCardCatalog interface {
	Agents(scope string) []api.AgentSummary
	AgentDefinition(key string) (catalog.AgentDefinition, bool)
	SkillDefinition(key string) (catalog.SkillDefinition, bool)
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
	tools   contracts.ToolDefinitionLookup
	options agentCardReporterOptions

	mu          sync.Mutex
	connections map[string]*agentCardConnection
	statuses    map[agentCardStatusKey]api.GatewayAgentCardReportStatus
	timer       *time.Timer
	rng         *mathrand.Rand
}

type agentCardConnection struct {
	gatewayID   string
	channelID   string
	conn        *ws.Conn
	ctx         context.Context
	cancel      context.CancelFunc
	cycleCancel context.CancelFunc
	generation  uint64
}

type agentCardStatusKey struct {
	channelID string
	agentKey  string
}

type builtAgentCard struct {
	agentKey string
	payload  api.GatewayAgentCardUpdatePayload
}

type cardBuildFailure struct {
	agentKey string
	err      error
}

type cardSendOutcome struct {
	accepted  bool
	rejected  bool
	retryable bool
	canceled  bool
	reason    string
}

func NewAgentCardReporter(ctx context.Context, source agentCardCatalog, tools contracts.ToolDefinitionLookup) *AgentCardReporter {
	return newAgentCardReporter(ctx, source, tools, agentCardReporterOptions{})
}

func newAgentCardReporter(ctx context.Context, source agentCardCatalog, tools contracts.ToolDefinitionLookup, options agentCardReporterOptions) *AgentCardReporter {
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
		ctx:         ctx,
		catalog:     source,
		tools:       tools,
		options:     options,
		connections: map[string]*agentCardConnection{},
		statuses:    map[agentCardStatusKey]api.GatewayAgentCardReportStatus{},
		rng:         mathrand.New(mathrand.NewSource(time.Now().UnixNano())),
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

func (r *AgentCardReporter) GatewayRegistered(gatewayID string, channelID string) {
	if r == nil {
		return
	}
	cards, failures := r.buildCards(channelID)
	r.mu.Lock()
	r.applyOfflineSnapshotLocked(strings.TrimSpace(channelID), cards, failures, time.Now().UnixMilli())
	r.mu.Unlock()
	for _, failure := range failures {
		log.Printf("[agent-card] build rejected: gateway=%s channel=%s agent=%s err=%s", strings.TrimSpace(gatewayID), strings.TrimSpace(channelID), failure.agentKey, sanitizeCardReason(failure.err.Error()))
	}
}

func (r *AgentCardReporter) GatewayConnected(gatewayID string, channelID string, conn *ws.Conn) {
	if r == nil || conn == nil || r.ctx.Err() != nil {
		return
	}
	gatewayID = strings.TrimSpace(gatewayID)
	channelID = strings.TrimSpace(channelID)
	connectionCtx, cancel := context.WithCancel(r.ctx)
	session := &agentCardConnection{
		gatewayID: gatewayID,
		channelID: channelID,
		conn:      conn,
		ctx:       connectionCtx,
		cancel:    cancel,
	}
	r.mu.Lock()
	if previous := r.connections[gatewayID]; previous != nil {
		previous.cancel()
	}
	r.connections[gatewayID] = session
	r.mu.Unlock()
	r.startCycle(session)
}

func (r *AgentCardReporter) GatewayDisconnected(gatewayID string, channelID string, conn *ws.Conn) {
	if r == nil {
		return
	}
	gatewayID = strings.TrimSpace(gatewayID)
	channelID = strings.TrimSpace(channelID)
	cards, failures := r.buildCards(channelID)
	now := time.Now().UnixMilli()
	r.mu.Lock()
	session := r.connections[gatewayID]
	if session == nil || session.conn != conn {
		r.mu.Unlock()
		return
	}
	session.cancel()
	delete(r.connections, gatewayID)
	r.applyOfflineSnapshotLocked(channelID, cards, failures, now)
	r.mu.Unlock()
}

func (r *AgentCardReporter) applyOfflineSnapshotLocked(channelID string, cards []builtAgentCard, failures []cardBuildFailure, now int64) {
	current := make(map[string]struct{}, len(cards)+len(failures))
	for _, card := range cards {
		current[card.agentKey] = struct{}{}
		key := agentCardStatusKey{channelID: channelID, agentKey: card.agentKey}
		status := r.statuses[key]
		status.Status = agentCardStatusOffline
		status.UpdatedAt = now
		status.Reason = ""
		r.statuses[key] = status
	}
	for _, failure := range failures {
		current[failure.agentKey] = struct{}{}
		r.statuses[agentCardStatusKey{channelID: channelID, agentKey: failure.agentKey}] = api.GatewayAgentCardReportStatus{
			Status:    agentCardStatusError,
			UpdatedAt: now,
			Reason:    sanitizeCardReason(failure.err.Error()),
		}
	}
	for key := range r.statuses {
		if key.channelID != channelID {
			continue
		}
		if _, exists := current[key.agentKey]; !exists {
			delete(r.statuses, key)
		}
	}
}

func (r *AgentCardReporter) AgentCardStatus(channelID string, externalAgentKey string) (api.GatewayAgentCardReportStatus, bool) {
	if r == nil {
		return api.GatewayAgentCardReportStatus{}, false
	}
	key := agentCardStatusKey{channelID: strings.TrimSpace(channelID), agentKey: strings.TrimSpace(externalAgentKey)}
	r.mu.Lock()
	status, ok := r.statuses[key]
	r.mu.Unlock()
	return status, ok
}

func (r *AgentCardReporter) refreshAll() {
	if r == nil || r.ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.timer = nil
	sessions := make([]*agentCardConnection, 0, len(r.connections))
	for _, session := range r.connections {
		sessions = append(sessions, session)
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
	if r.connections[session.gatewayID] != session {
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
	r.mu.Unlock()
	go r.reportCycle(cycleCtx, session, generation)
}

func (r *AgentCardReporter) reportCycle(ctx context.Context, session *agentCardConnection, generation uint64) {
	cards, failures := r.buildCards(session.channelID)
	currentKeys := make(map[string]struct{}, len(cards)+len(failures))
	for _, card := range cards {
		currentKeys[card.agentKey] = struct{}{}
	}
	for _, failure := range failures {
		currentKeys[failure.agentKey] = struct{}{}
	}
	r.pruneStatuses(session, generation, currentKeys)
	for _, failure := range failures {
		reason := sanitizeCardReason(failure.err.Error())
		r.setStatus(session, generation, failure.agentKey, api.GatewayAgentCardReportStatus{
			Status:    agentCardStatusError,
			UpdatedAt: time.Now().UnixMilli(),
			Reason:    reason,
		})
		log.Printf("[agent-card] build rejected: gateway=%s channel=%s agent=%s err=%s", session.gatewayID, session.channelID, failure.agentKey, reason)
	}
	if len(cards) == 0 || ctx.Err() != nil {
		return
	}
	semaphore := make(chan struct{}, r.options.MaxConcurrent)
	var wg sync.WaitGroup
	for _, card := range cards {
		card := card
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			r.reportCard(ctx, session, generation, card)
		}()
	}
	wg.Wait()
}

func (r *AgentCardReporter) reportCard(ctx context.Context, session *agentCardConnection, generation uint64, card builtAgentCard) {
	maxAttempts := len(r.options.RetryDelays) + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		requestID := newAgentCardRequestID()
		state := agentCardStatusPending
		if attempt > 1 {
			state = agentCardStatusRetrying
		}
		r.setStatus(session, generation, card.agentKey, api.GatewayAgentCardReportStatus{
			Status:    state,
			RequestID: requestID,
			Attempt:   attempt,
			UpdatedAt: time.Now().UnixMilli(),
		})
		outcome := r.sendCardOnce(ctx, session.conn, requestID, card.payload)
		if outcome.canceled {
			return
		}
		if outcome.accepted {
			now := time.Now().UnixMilli()
			r.setStatus(session, generation, card.agentKey, api.GatewayAgentCardReportStatus{
				Status:     agentCardStatusAccepted,
				RequestID:  requestID,
				Attempt:    attempt,
				UpdatedAt:  now,
				AcceptedAt: now,
			})
			return
		}
		if outcome.rejected {
			r.setStatus(session, generation, card.agentKey, api.GatewayAgentCardReportStatus{
				Status:    agentCardStatusRejected,
				RequestID: requestID,
				Attempt:   attempt,
				UpdatedAt: time.Now().UnixMilli(),
				Reason:    sanitizeCardReason(outcome.reason),
			})
			log.Printf("[agent-card] gateway rejected card: gateway=%s channel=%s agent=%s reason=%s", session.gatewayID, session.channelID, card.agentKey, sanitizeCardReason(outcome.reason))
			return
		}
		if !outcome.retryable || attempt == maxAttempts {
			reason := sanitizeCardReason(outcome.reason)
			r.setStatus(session, generation, card.agentKey, api.GatewayAgentCardReportStatus{
				Status:    agentCardStatusError,
				RequestID: requestID,
				Attempt:   attempt,
				UpdatedAt: time.Now().UnixMilli(),
				Reason:    reason,
			})
			log.Printf("[agent-card] report failed: gateway=%s channel=%s agent=%s attempt=%d err=%s", session.gatewayID, session.channelID, card.agentKey, attempt, reason)
			return
		}
		reason := sanitizeCardReason(outcome.reason)
		r.setStatus(session, generation, card.agentKey, api.GatewayAgentCardReportStatus{
			Status:    agentCardStatusRetrying,
			RequestID: requestID,
			Attempt:   attempt,
			UpdatedAt: time.Now().UnixMilli(),
			Reason:    reason,
		})
		delay := r.jitter(r.options.RetryDelays[attempt-1])
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *AgentCardReporter) sendCardOnce(ctx context.Context, conn *ws.Conn, requestID string, payload api.GatewayAgentCardUpdatePayload) cardSendOutcome {
	raw, err := json.Marshal(payload)
	if err != nil {
		return cardSendOutcome{reason: err.Error()}
	}
	frames, cleanup, err := conn.OpenOutboundRequest(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    agentCardUpdateType,
		ID:      requestID,
		Payload: raw,
	})
	if err != nil {
		return cardSendOutcome{retryable: true, reason: err.Error()}
	}
	defer cleanup()
	timer := time.NewTimer(r.options.AckTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return cardSendOutcome{canceled: true}
	case <-conn.Done():
		return cardSendOutcome{canceled: true}
	case <-timer.C:
		return cardSendOutcome{retryable: true, reason: "agent card acknowledgement timed out"}
	case data, ok := <-frames:
		if !ok {
			return cardSendOutcome{retryable: true, reason: "gateway connection closed before acknowledgement"}
		}
		return decodeAgentCardResponse(data, requestID, payload.AgentKey)
	}
}

func decodeAgentCardResponse(data []byte, requestID string, agentKey string) cardSendOutcome {
	var frame struct {
		Frame string          `json:"frame"`
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Code  int             `json:"code"`
		Msg   string          `json:"msg"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return cardSendOutcome{reason: "invalid agent card acknowledgement"}
	}
	if strings.TrimSpace(frame.ID) != requestID {
		return cardSendOutcome{reason: "agent card acknowledgement id mismatch"}
	}
	switch strings.ToLower(strings.TrimSpace(frame.Frame)) {
	case ws.FrameError:
		reason := firstCardReason(frame.Msg, "gateway rejected agent card request")
		return cardSendOutcome{retryable: frame.Code >= 500, rejected: frame.Code > 0 && frame.Code < 500, reason: reason}
	case ws.FrameResponse:
		if strings.TrimSpace(frame.Type) != agentCardUpdateType {
			return cardSendOutcome{reason: "agent card acknowledgement type mismatch"}
		}
		if frame.Code != 0 {
			reason := firstCardReason(frame.Msg, "gateway returned a non-zero response code")
			return cardSendOutcome{retryable: frame.Code >= 500, rejected: frame.Code > 0 && frame.Code < 500, reason: reason}
		}
		var ack api.GatewayAgentCardAck
		if err := json.Unmarshal(frame.Data, &ack); err != nil || ack.Accepted == nil {
			return cardSendOutcome{reason: "agent card acknowledgement data is invalid"}
		}
		if strings.TrimSpace(ack.AgentKey) != agentKey {
			return cardSendOutcome{reason: "agent card acknowledgement agentKey mismatch"}
		}
		if !*ack.Accepted {
			return cardSendOutcome{rejected: true, reason: firstCardReason(ack.Reason, frame.Msg, "gateway did not accept the agent card")}
		}
		return cardSendOutcome{accepted: true}
	default:
		return cardSendOutcome{reason: "unexpected agent card acknowledgement frame"}
	}
}

func (r *AgentCardReporter) buildCards(channelID string) ([]builtAgentCard, []cardBuildFailure) {
	if r == nil || r.catalog == nil {
		return nil, nil
	}
	channelID = strings.TrimSpace(channelID)
	cardsByKey := map[string]builtAgentCard{}
	failuresByKey := map[string]cardBuildFailure{}
	for _, summary := range r.catalog.Agents("all") {
		def, ok := r.catalog.AgentDefinition(summary.Key)
		if !ok || catalog.AgentIsChannelMode(def.Mode) {
			continue
		}
		for _, export := range def.ChannelConfig.Exports {
			if strings.TrimSpace(export.ChannelID) != channelID || !export.Allow.Query {
				continue
			}
			externalKey := catalog.EffectiveChannelExportExternalKey(def.Key, export)
			if externalKey == "" {
				continue
			}
			if err := validateCardText("agentKey", externalKey, defaultCardNameRunes, true); err != nil {
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: err}
				continue
			}
			if _, duplicate := cardsByKey[externalKey]; duplicate {
				delete(cardsByKey, externalKey)
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: fmt.Errorf("duplicate external agent key on channel")}
				continue
			}
			if _, duplicate := failuresByKey[externalKey]; duplicate {
				continue
			}
			card, err := r.buildCard(def, externalKey)
			if err != nil {
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: err}
				continue
			}
			built := builtAgentCard{
				agentKey: externalKey,
				payload:  api.GatewayAgentCardUpdatePayload{AgentKey: externalKey, AgentCard: card},
			}
			if err := r.validateRequestSize(built.payload); err != nil {
				failuresByKey[externalKey] = cardBuildFailure{agentKey: externalKey, err: err}
				continue
			}
			cardsByKey[externalKey] = built
		}
	}
	cards := make([]builtAgentCard, 0, len(cardsByKey))
	for _, card := range cardsByKey {
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].agentKey < cards[j].agentKey })
	failures := make([]cardBuildFailure, 0, len(failuresByKey))
	for _, failure := range failuresByKey {
		failures = append(failures, failure)
	}
	sort.Slice(failures, func(i, j int) bool { return failures[i].agentKey < failures[j].agentKey })
	return cards, failures
}

func (r *AgentCardReporter) validateRequestSize(payload api.GatewayAgentCardUpdatePayload) error {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rawFrame, err := json.Marshal(ws.RequestFrame{
		Frame:   ws.FrameRequest,
		Type:    agentCardUpdateType,
		ID:      "card_000000000000000000000000",
		Payload: rawPayload,
	})
	if err != nil {
		return err
	}
	if len(rawFrame) > r.options.MaxCardBytes {
		return fmt.Errorf("agent card request exceeds %d bytes", r.options.MaxCardBytes)
	}
	return nil
}

func (r *AgentCardReporter) buildCard(def catalog.AgentDefinition, externalKey string) (api.GatewayAgentCard, error) {
	card := api.GatewayAgentCard{
		Name:        strings.TrimSpace(def.Name),
		Description: strings.TrimSpace(def.Description),
		Skills:      []api.GatewayAgentCardFeature{},
		Tools:       []api.GatewayAgentCardFeature{},
	}
	if card.Name == "" {
		card.Name = strings.TrimSpace(def.Key)
	}
	if err := validateCardText("agentCard.name", card.Name, defaultCardNameRunes, true); err != nil {
		return api.GatewayAgentCard{}, err
	}
	if err := validateCardText("agentCard.description", card.Description, defaultCardDescriptionRunes, false); err != nil {
		return api.GatewayAgentCard{}, err
	}

	skills := map[string]api.GatewayAgentCardFeature{}
	for _, skillID := range def.Skills {
		skill, ok := r.catalog.SkillDefinition(strings.TrimSpace(skillID))
		if !ok {
			return api.GatewayAgentCard{}, fmt.Errorf("skill %q is not available in the catalog", skillID)
		}
		tags, err := cardTags(skill.Metadata["tags"])
		if err != nil {
			return api.GatewayAgentCard{}, fmt.Errorf("skill %q tags: %w", skillID, err)
		}
		feature := api.GatewayAgentCardFeature{
			ID:          strings.TrimSpace(skill.Key),
			Name:        firstCardReason(skill.Name, skill.Key),
			Description: strings.TrimSpace(skill.Description),
			Tags:        tags,
		}
		if err := validateCardFeature("skill", feature); err != nil {
			return api.GatewayAgentCard{}, err
		}
		skills[feature.ID] = feature
	}
	if def.KBaseConfig.Enabled || strings.EqualFold(strings.TrimSpace(def.Mode), catalog.AgentModeKBase) {
		tags, err := cardTags(def.KBaseConfig.Tags)
		if err != nil {
			return api.GatewayAgentCard{}, fmt.Errorf("kbase tags: %w", err)
		}
		feature := api.GatewayAgentCardFeature{
			ID:          "kb.query." + strings.TrimSpace(externalKey),
			Name:        "Knowledge Base Query",
			Description: "Query the local knowledge base exposed by this agent.",
			Tags:        tags,
		}
		if _, exists := skills[feature.ID]; !exists {
			if err := validateCardFeature("skill", feature); err != nil {
				return api.GatewayAgentCard{}, err
			}
			skills[feature.ID] = feature
		}
	}
	card.Skills = sortedCardFeatures(skills)

	tools := map[string]api.GatewayAgentCardFeature{}
	for _, toolName := range def.Tools {
		if r.tools == nil {
			return api.GatewayAgentCard{}, fmt.Errorf("tool catalog is not available")
		}
		tool, ok := r.tools.Tool(strings.TrimSpace(toolName))
		if !ok {
			return api.GatewayAgentCard{}, fmt.Errorf("tool %q is not available in the catalog", toolName)
		}
		if toolIsPrivate(tool.Meta) {
			continue
		}
		tags, err := cardTags(tool.Meta["tags"])
		if err != nil {
			return api.GatewayAgentCard{}, fmt.Errorf("tool %q tags: %w", toolName, err)
		}
		feature := api.GatewayAgentCardFeature{
			ID:          firstCardReason(tool.Name, tool.Key),
			Name:        firstCardReason(tool.Label, tool.Name, tool.Key),
			Description: strings.TrimSpace(tool.Description),
			Tags:        tags,
		}
		if err := validateCardFeature("tool", feature); err != nil {
			return api.GatewayAgentCard{}, err
		}
		tools[feature.ID] = feature
	}
	card.Tools = sortedCardFeatures(tools)
	if len(card.Skills) > defaultCardMaxFeatures || len(card.Tools) > defaultCardMaxFeatures {
		return api.GatewayAgentCard{}, fmt.Errorf("agent card contains too many skills or tools")
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return api.GatewayAgentCard{}, err
	}
	if len(encoded) > r.options.MaxCardBytes {
		return api.GatewayAgentCard{}, fmt.Errorf("agent card exceeds %d bytes", r.options.MaxCardBytes)
	}
	return card, nil
}

func validateCardFeature(kind string, feature api.GatewayAgentCardFeature) error {
	if err := validateCardText(kind+".id", feature.ID, defaultCardNameRunes, true); err != nil {
		return err
	}
	if err := validateCardText(kind+".name", feature.Name, defaultCardNameRunes, true); err != nil {
		return err
	}
	if err := validateCardText(kind+".description", feature.Description, defaultCardDescriptionRunes, false); err != nil {
		return err
	}
	if len(feature.Tags) > defaultCardMaxTags {
		return fmt.Errorf("%s %q contains too many tags", kind, feature.ID)
	}
	for _, tag := range feature.Tags {
		if err := validateCardText(kind+".tag", tag, defaultCardTagRunes, true); err != nil {
			return err
		}
	}
	return nil
}

func validateCardText(field string, value string, maxRunes int, required bool) error {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains invalid control characters", field)
	}
	if cardCredentialPattern.MatchString(value) {
		return fmt.Errorf("%s contains credential-like content", field)
	}
	if containsCardAbsolutePath(value) {
		return fmt.Errorf("%s contains an absolute local path", field)
	}
	return nil
}

func containsCardAbsolutePath(value string) bool {
	for _, match := range cardAbsolutePathPattern.FindAllString(value, -1) {
		// Bundled public documentation uses ellipsis-only paths as examples;
		// these cannot disclose a real local location.
		if !strings.Contains(match, "...") {
			return true
		}
	}
	return false
}

func sortedCardFeatures(values map[string]api.GatewayAgentCardFeature) []api.GatewayAgentCardFeature {
	items := make([]api.GatewayAgentCardFeature, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func cardTags(value any) ([]string, error) {
	var raw []string
	switch typed := value.(type) {
	case nil:
	case []string:
		raw = append(raw, typed...)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("must contain only strings")
			}
			raw = append(raw, text)
		}
	case string:
		raw = append(raw, splitCardTagText(typed)...)
	default:
		return nil, fmt.Errorf("must be a string or list of strings")
	}
	seen := map[string]struct{}{}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	sort.Strings(items)
	return items, nil
}

func splitCardTagText(value string) []string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
		parts := strings.Split(value, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.Trim(strings.TrimSpace(part), `"'`)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	return []string{value}
}

func toolIsPrivate(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	if internal, ok := meta["internalOnly"].(bool); ok && internal {
		return true
	}
	if visible, ok := meta["catalogVisible"].(bool); ok && !visible {
		return true
	}
	return false
}

func (r *AgentCardReporter) pruneStatuses(session *agentCardConnection, generation uint64, current map[string]struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connections[session.gatewayID] != session || session.generation != generation {
		return
	}
	for key := range r.statuses {
		if key.channelID != session.channelID {
			continue
		}
		if _, exists := current[key.agentKey]; !exists {
			delete(r.statuses, key)
		}
	}
}

func (r *AgentCardReporter) setStatus(session *agentCardConnection, generation uint64, agentKey string, status api.GatewayAgentCardReportStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connections[session.gatewayID] != session || session.generation != generation {
		return
	}
	status.Reason = sanitizeCardReason(status.Reason)
	r.statuses[agentCardStatusKey{channelID: session.channelID, agentKey: strings.TrimSpace(agentKey)}] = status
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
		return "card_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("card_%x", time.Now().UnixNano())
}

func sanitizeCardReason(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = cardCredentialPattern.ReplaceAllString(value, "[redacted credential]")
	value = cardAbsolutePathPattern.ReplaceAllString(value, " [redacted local path]")
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
