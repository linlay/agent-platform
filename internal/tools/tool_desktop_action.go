package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

const desktopCdpCaptureScreenshotMethod = "Page.captureScreenshot"
const desktopCdpScreenshotMimeType = "image/png"

const (
	desktopCDPRequestType           = "desktop.cdp.call"
	desktopResponseDeltaEventType   = "desktop.bridge.response.delta"
	desktopScreenshotDeltaEventType = "desktop.cdp.screenshot.delta"
	desktopMaxDecodedResponseBytes  = 64 << 20
)

var desktopCdpBooleanParamKeys = map[string]bool{
	"allowUnsafeEvalBlockedByCSP": true,
	"autoAttach":                  true,
	"autoRepeat":                  true,
	"awaitPromise":                true,
	"background":                  true,
	"captureBeyondViewport":       true,
	"disableBreaks":               true,
	"discover":                    true,
	"dontSetVisibleSize":          true,
	"exclude":                     true,
	"flatten":                     true,
	"fromSurface":                 true,
	"generatePreview":             true,
	"hasTouch":                    true,
	"hidden":                      true,
	"ignoreCache":                 true,
	"includeCommandLineAPI":       true,
	"isKeypad":                    true,
	"isMobile":                    true,
	"isSystemKey":                 true,
	"landscape":                   true,
	"newWindow":                   true,
	"optimizeForSpeed":            true,
	"pierce":                      true,
	"replMode":                    true,
	"reportDirectSocketTraffic":   true,
	"returnByValue":               true,
	"silent":                      true,
	"throwOnSideEffect":           true,
	"userGesture":                 true,
	"waitForDebuggerOnStart":      true,
}

var (
	desktopActionAllowlistOnce sync.Once
	desktopActionAllowlist     map[string]bool
	desktopActionAllowlistErr  error
	desktopRequestSeq          atomic.Uint64
)

var desktopActionReservedArgFields = []string{
	"source",
	"confirmation" + "Summary",
}

func getDesktopActionAllowlist() (map[string]bool, error) {
	desktopActionAllowlistOnce.Do(func() {
		desktopActionAllowlist, desktopActionAllowlistErr = loadDesktopActionAllowlist()
	})
	return desktopActionAllowlist, desktopActionAllowlistErr
}

func loadDesktopActionAllowlist() (map[string]bool, error) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		if def.Name != "desktop_action" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("desktop_action schema missing properties")
		}
		actionProperty, ok := properties["action"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("desktop_action schema missing action property")
		}
		enum, ok := actionProperty["enum"].([]any)
		if !ok || len(enum) == 0 {
			return nil, fmt.Errorf("desktop_action action enum is required")
		}
		allowlist := make(map[string]bool, len(enum))
		for _, item := range enum {
			action, ok := item.(string)
			if !ok || strings.TrimSpace(action) == "" {
				return nil, fmt.Errorf("desktop_action action enum contains an invalid value")
			}
			action = strings.TrimSpace(action)
			if allowlist[action] {
				return nil, fmt.Errorf("desktop_action action enum contains duplicate value %q", action)
			}
			allowlist[action] = true
		}
		return allowlist, nil
	}
	return nil, fmt.Errorf("desktop_action tool definition not found")
}

type desktopCDPRequest struct {
	RequestID string           `json:"requestId,omitempty"`
	Method    string           `json:"method"`
	Params    map[string]any   `json:"params,omitempty"`
	TargetID  string           `json:"targetId,omitempty"`
	SessionID string           `json:"sessionId,omitempty"`
	SurfaceID string           `json:"surfaceId,omitempty"`
	Source    desktopCDPSource `json:"source,omitempty"`
}

type desktopCDPSource struct {
	RunID    string `json:"runId,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	AgentKey string `json:"agentKey,omitempty"`
	TeamID   string `json:"teamId,omitempty"`
}

func (t *RuntimeToolExecutor) invokeDesktopAction(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	action := strings.TrimSpace(stringArg(args, "action"))
	if action == "" {
		return desktopActionErrorResult("invalid_args", "action is required", nil), nil
	}
	allowlist, err := getDesktopActionAllowlist()
	if err != nil {
		return desktopActionErrorResult("desktop_action_allowlist_unavailable", "desktop action allowlist is unavailable", map[string]any{"error": err.Error()}), nil
	}
	if !allowlist[action] {
		return desktopActionErrorResult("unknown_action", "desktop action is not allowlisted", map[string]any{"action": action}), nil
	}

	actionArgs, ok := args["args"].(map[string]any)
	if !ok || actionArgs == nil {
		actionArgs = map[string]any{}
	}
	for _, reserved := range desktopActionReservedArgFields {
		if _, exists := actionArgs[reserved]; exists {
			return desktopActionErrorResult("invalid_args", fmt.Sprintf("args.%s is reserved", reserved), map[string]any{"field": reserved}), nil
		}
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop && !strings.HasPrefix(action, "desktop.workpanel.") && action != "desktop.display" {
		return desktopActionErrorResult("desktop_action_unsupported_runtime", "desktop action is unavailable in standalone runtime mode", map[string]any{"action": action}), nil
	}
	requestID := strings.TrimSpace(stringArg(args, "requestId"))
	if requestID == "" {
		requestID = newDesktopRequestID("dsa")
	}

	source, sourceErr := buildDesktopActionSource(execCtx)
	if sourceErr != nil {
		return desktopActionErrorResult("invalid_execution_context", sourceErr.Error(), nil), nil
	}
	return t.invokeDesktopClientRequest(ctx, requestID, action, actionArgs, &source, "desktop_action", false, execCtx)
}

func firstDesktopActionMessage(message string, fallback string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	return fallback
}

func (t *RuntimeToolExecutor) invokeDesktopCDP(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	method := strings.TrimSpace(stringArg(args, "method"))
	if method == "" {
		return desktopActionErrorResult("invalid_args", "method is required", nil), nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return desktopActionErrorResult("desktop_cdp_unsupported_runtime", "desktop_cdp is unavailable in standalone runtime mode", nil), nil
	}
	params, ok := args["params"].(map[string]any)
	if !ok || params == nil {
		params = map[string]any{}
	}
	requestID := strings.TrimSpace(stringArg(args, "requestId"))
	if requestID == "" {
		requestID = newDesktopRequestID("dsc")
	}
	payload := desktopCDPRequest{
		RequestID: requestID,
		Method:    method,
		Params:    normalizeDesktopCDPParams(params),
		TargetID:  strings.TrimSpace(stringArg(args, "targetId")),
		SessionID: strings.TrimSpace(stringArg(args, "sessionId")),
		SurfaceID: strings.TrimSpace(stringArg(args, "surfaceId")),
		Source:    buildDesktopCDPSource(execCtx),
	}
	return t.invokeDesktopClientRequest(ctx, requestID, desktopCDPRequestType, payload, nil, "desktop_cdp", method == desktopCdpCaptureScreenshotMethod, execCtx)
}

func newDesktopRequestID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), desktopRequestSeq.Add(1))
}

func normalizeDesktopCDPParams(params map[string]any) map[string]any {
	normalized := make(map[string]any, len(params))
	for key, value := range params {
		normalized[key] = normalizeDesktopCDPParamValue(key, value)
	}
	return normalized
}

func normalizeDesktopCDPParamValue(key string, value any) any {
	if desktopCdpBooleanParamKeys[key] {
		if boolValue, ok := parseDesktopCDPStringBool(value); ok {
			return boolValue
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		return normalizeDesktopCDPParams(typed)
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeDesktopCDPParamValue("", item)
		}
		return normalized
	default:
		return value
	}
}

func parseDesktopCDPStringBool(value any) (bool, bool) {
	raw, ok := value.(string)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (t *RuntimeToolExecutor) invokeDesktopClientRequest(ctx context.Context, requestID string, requestType string, payload any, source *ClientRequestSource, toolName string, screenshot bool, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t == nil || t.clientRequest == nil {
		return desktopActionErrorResult(toolName+"_provider_unavailable", "desktop client request provider is unavailable", nil), nil
	}
	target, unavailableReason := t.resolveClientTarget(execCtx)
	if target.IsZero() {
		return desktopActionErrorResult(toolName+"_target_unavailable", "client target is unavailable for this run", map[string]any{"reason": unavailableReason}), nil
	}
	payloadMap, err := desktopPayloadMap(payload)
	if err != nil {
		return desktopActionErrorResult("invalid_args", err.Error(), nil), nil
	}
	collector := newDesktopResponseCollector(t, screenshot, execCtx)
	defer collector.abort()
	request := ClientRequest{ID: requestID, Type: requestType, Source: source, Payload: payloadMap}
	err = t.clientRequest.InvokeClientRequest(ctx, target, request, collector.consume)
	if errors.Is(err, ErrClientTargetUnavailable) && t.cfg.RuntimeMode == config.RuntimeModeDesktop {
		refreshedTarget, refreshedReason := t.resolveDesktopMainTarget(execCtx)
		if !refreshedTarget.IsZero() && refreshedTarget != target {
			target = refreshedTarget
			unavailableReason = ""
			err = t.clientRequest.InvokeClientRequest(ctx, target, request, collector.consume)
		} else if refreshedReason != "" {
			unavailableReason = refreshedReason
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return desktopActionErrorResult(toolName+"_client_timeout", "client request timed out", nil), nil
		case errors.Is(err, ErrClientTargetUnavailable):
			if unavailableReason == "" {
				unavailableReason = "target_connection_unavailable"
			}
			return desktopActionErrorResult(toolName+"_target_unavailable", "client target is unavailable for this run", map[string]any{"reason": unavailableReason}), nil
		case errors.Is(err, ErrClientDisconnected):
			return desktopActionErrorResult(toolName+"_client_disconnected", "client disconnected before completing the request", nil), nil
		default:
			return desktopActionErrorResult(toolName+"_invalid_client_response", err.Error(), nil), nil
		}
	}
	frame := collector.final
	if frame == nil {
		return desktopActionErrorResult(toolName+"_invalid_client_response", "client response is missing", nil), nil
	}
	if strings.EqualFold(frame.Frame, "error") {
		if strings.TrimSpace(frame.Type) == "" || frame.Code == nil || *frame.Code <= 0 {
			return desktopActionErrorResult(toolName+"_invalid_client_response", "client error frame type or code is invalid", nil), nil
		}
		return desktopActionErrorResult(
			toolName+"_client_rejected",
			firstDesktopActionMessage(frame.Msg, "client rejected the request"),
			desktopClientRejectionDetails(*frame),
		), nil
	}
	if frame.Type != requestType || frame.Code == nil {
		return desktopActionErrorResult(toolName+"_invalid_client_response", "client response type or code is invalid", nil), nil
	}
	if *frame.Code != 0 {
		return desktopActionErrorResult(toolName+"_client_rejected", firstDesktopActionMessage(frame.Msg, "client rejected the request"), map[string]any{"clientCode": *frame.Code}), nil
	}
	decoded, err := collector.responseData(frame.Data)
	if err != nil {
		return desktopActionErrorResult(toolName+"_invalid_client_response", err.Error(), nil), nil
	}
	structured := map[string]any{"transport": "reverse-websocket", "response": decoded}
	result := structuredResultWithExit(structured, 0)
	if decoded["ok"] == false {
		result = structuredResultWithExit(structured, -1)
	}
	if screenshot && result.ExitCode == 0 {
		if collector.screenshot != nil {
			return collector.commitScreenshotResult(structured), nil
		}
		return t.storeDesktopCdpScreenshot(result, execCtx), nil
	}
	return result, nil
}

func desktopClientRejectionDetails(frame ClientResponseFrame) map[string]any {
	details := map[string]any{
		"clientErrorType": frame.Type,
		"clientCode":      *frame.Code,
	}
	if len(frame.Data) == 0 {
		return details
	}
	var metadata struct {
		Retryable *bool          `json:"retryable"`
		Details   map[string]any `json:"details"`
	}
	if err := json.Unmarshal(frame.Data, &metadata); err != nil {
		return details
	}
	if metadata.Retryable != nil {
		details["retryable"] = *metadata.Retryable
	}
	for _, key := range []string{"recovery", "reason"} {
		if value, ok := metadata.Details[key].(string); ok && strings.TrimSpace(value) != "" {
			details[key] = strings.TrimSpace(value)
		}
	}
	return details
}

func (t *RuntimeToolExecutor) resolveClientTarget(execCtx *ExecutionContext) (ClientTarget, string) {
	if execCtx == nil {
		return ClientTarget{}, "run_target_missing"
	}
	if t.clientTargets != nil {
		if current, ok := t.clientTargets.ResolveClientTarget(execCtx.Session.RunID); ok {
			return current, ""
		}
		if t.cfg.RuntimeMode == config.RuntimeModeDesktop {
			return t.resolveDesktopMainTarget(execCtx)
		}
		return ClientTarget{}, "run_target_missing"
	}
	if !execCtx.Session.WebClientTarget.IsZero() {
		return execCtx.Session.WebClientTarget, ""
	}
	if t.cfg.RuntimeMode == config.RuntimeModeDesktop {
		return t.resolveDesktopMainTarget(execCtx)
	}
	return ClientTarget{}, "run_target_missing"
}

func (t *RuntimeToolExecutor) resolveDesktopMainTarget(execCtx *ExecutionContext) (ClientTarget, string) {
	if t == nil || t.desktopMain == nil {
		return ClientTarget{}, "desktop_main_missing"
	}
	target, state := t.desktopMain.ResolveDesktopMainTarget()
	switch state {
	case DesktopMainTargetMissing:
		return ClientTarget{}, "desktop_main_missing"
	case DesktopMainTargetDisconnected:
		return ClientTarget{}, "desktop_main_disconnected"
	case DesktopMainTargetReady:
		if target.IsZero() {
			return ClientTarget{}, "desktop_main_disconnected"
		}
	default:
		return ClientTarget{}, "desktop_main_missing"
	}
	if execCtx == nil || strings.TrimSpace(execCtx.Session.RunID) == "" || t.clientTargets == nil ||
		!t.clientTargets.BindClientTarget(execCtx.Session.RunID, target) {
		return ClientTarget{}, "run_target_missing"
	}
	return target, ""
}

func desktopPayloadMap(payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

type desktopStreamEvent struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Encoding  string `json:"encoding"`
	Chunk     string `json:"chunk"`
}

type desktopResponseCollector struct {
	executor           *RuntimeToolExecutor
	screenshotExpected bool
	execCtx            *ExecutionContext
	streamID           string
	nextSeq            int64
	streamType         string
	response           bytes.Buffer
	screenshot         *desktopScreenshotSink
	final              *ClientResponseFrame
}

func newDesktopResponseCollector(executor *RuntimeToolExecutor, screenshotExpected bool, execCtx *ExecutionContext) *desktopResponseCollector {
	return &desktopResponseCollector{executor: executor, screenshotExpected: screenshotExpected, execCtx: execCtx, nextSeq: 1}
}

func (c *desktopResponseCollector) consume(frame ClientResponseFrame) error {
	switch strings.ToLower(strings.TrimSpace(frame.Frame)) {
	case "response", "error":
		if c.final != nil {
			return errors.New("client returned multiple terminal frames")
		}
		copyFrame := frame
		c.final = &copyFrame
		return nil
	case "stream":
		if c.final != nil {
			return errors.New("client returned a stream frame after the terminal response")
		}
	default:
		return fmt.Errorf("unsupported client frame %q", frame.Frame)
	}
	if strings.TrimSpace(frame.StreamID) == "" {
		return errors.New("desktop stream frame requires streamId")
	}
	if c.streamID == "" {
		c.streamID = frame.StreamID
	} else if c.streamID != frame.StreamID {
		return errors.New("desktop streamId changed during the response")
	}
	var event desktopStreamEvent
	if len(frame.Event) == 0 || json.Unmarshal(frame.Event, &event) != nil {
		return errors.New("desktop stream event is invalid")
	}
	if event.Seq != c.nextSeq {
		return fmt.Errorf("desktop stream seq is out of order: got %d want %d", event.Seq, c.nextSeq)
	}
	c.nextSeq++
	if event.Timestamp <= 0 {
		return errors.New("desktop stream event requires epoch-millisecond timestamp")
	}
	if event.Encoding != "base64" || event.Chunk == "" {
		return errors.New("desktop stream event requires a non-empty base64 chunk")
	}
	if c.streamType == "" {
		c.streamType = event.Type
	} else if c.streamType != event.Type {
		return errors.New("desktop stream event type changed during the response")
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Chunk)
	if err != nil {
		return fmt.Errorf("decode desktop stream chunk: %w", err)
	}
	switch event.Type {
	case desktopResponseDeltaEventType:
		if c.response.Len()+len(decoded) > desktopMaxDecodedResponseBytes {
			return errors.New("desktop response exceeds 64 MiB")
		}
		_, err = c.response.Write(decoded)
		return err
	case desktopScreenshotDeltaEventType:
		if !c.screenshotExpected {
			return errors.New("unexpected desktop screenshot stream")
		}
		if c.screenshot == nil {
			c.screenshot, err = newDesktopScreenshotSink(c.executor, c.execCtx)
			if err != nil {
				return err
			}
		}
		return c.screenshot.write(decoded)
	default:
		return fmt.Errorf("unsupported desktop stream event type %q", event.Type)
	}
}

func (c *desktopResponseCollector) responseData(raw json.RawMessage) (map[string]any, error) {
	if c.response.Len() > 0 {
		if c.streamType != desktopResponseDeltaEventType {
			return nil, errors.New("desktop response stream type is invalid")
		}
		manifest, err := decodeDesktopResponseMap(raw)
		if err != nil {
			return nil, err
		}
		if err := c.validateStreamManifest(manifest, int64(c.response.Len())); err != nil {
			return nil, err
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(c.response.Bytes(), &decoded); err != nil {
			return nil, fmt.Errorf("decode streamed desktop response: %w", err)
		}
		return decoded, nil
	}
	decoded, err := decodeDesktopResponseMap(raw)
	if err != nil {
		return nil, err
	}
	if c.screenshot != nil {
		if c.streamType != desktopScreenshotDeltaEventType {
			return nil, errors.New("desktop screenshot stream type is invalid")
		}
		resultNode := cloneDesktopMapValue(decoded["result"])
		manifest := cloneDesktopMapValue(resultNode["data"])
		if err := c.validateStreamManifest(manifest, c.screenshot.size); err != nil {
			return nil, err
		}
	} else if c.streamID != "" {
		return nil, errors.New("desktop stream completed without decoded content")
	}
	return decoded, nil
}

func decodeDesktopResponseMap(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("desktop response data must be an object")
	}
	return decoded, nil
}

func (c *desktopResponseCollector) validateStreamManifest(manifest map[string]any, totalBytes int64) error {
	if manifest["streamed"] != true {
		return errors.New("desktop stream terminal response requires streamed=true")
	}
	if streamID, _ := manifest["streamId"].(string); strings.TrimSpace(streamID) == "" || streamID != c.streamID {
		return errors.New("desktop stream terminal response has an invalid streamId")
	}
	if encoding, _ := manifest["encoding"].(string); encoding != "base64" {
		return errors.New("desktop stream terminal response has an invalid encoding")
	}
	if c.streamType == desktopResponseDeltaEventType {
		if contentType, _ := manifest["contentType"].(string); contentType != "application/json" {
			return errors.New("desktop stream terminal response has an invalid contentType")
		}
	}
	chunkCount, ok := desktopManifestInteger(manifest["chunkCount"])
	if !ok || chunkCount != c.nextSeq-1 {
		return errors.New("desktop stream terminal response has an invalid chunkCount")
	}
	manifestBytes, ok := desktopManifestInteger(manifest["totalBytes"])
	if !ok || manifestBytes != totalBytes {
		return errors.New("desktop stream terminal response has an invalid totalBytes")
	}
	return nil
}

func desktopManifestInteger(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number != math.Trunc(number) || number > math.MaxInt64 {
		return 0, false
	}
	return int64(number), true
}

func (c *desktopResponseCollector) commitScreenshotResult(structured map[string]any) ToolExecutionResult {
	response := cloneDesktopMapValue(structured["response"])
	resultNode := cloneDesktopMapValue(response["result"])
	if len(resultNode) == 0 {
		return desktopCdpScreenshotErrorResult(structured, "desktop_cdp_screenshot_data_missing", "Page.captureScreenshot response.result is missing", nil)
	}
	metadata, err := c.screenshot.commit()
	if err != nil {
		return desktopCdpScreenshotErrorResult(structured, "desktop_cdp_screenshot_write_failed", err.Error(), nil)
	}
	resultNode["data"] = metadata
	response["result"] = resultNode
	structured["response"] = response
	return structuredResult(structured)
}

func (c *desktopResponseCollector) abort() {
	if c != nil && c.screenshot != nil {
		c.screenshot.abort()
	}
}

type desktopScreenshotSink struct {
	file          *os.File
	temporaryPath string
	finalPath     string
	referenceName string
	hash          hash.Hash
	size          int64
	committed     bool
}

func newDesktopScreenshotSink(executor *RuntimeToolExecutor, execCtx *ExecutionContext) (*desktopScreenshotSink, error) {
	chatID := desktopCdpScreenshotChatID(execCtx)
	if executor == nil || strings.TrimSpace(executor.cfg.Paths.ChatsDir) == "" || !chat.ValidChatID(chatID) {
		return nil, errors.New("chat context and cfg.Paths.ChatsDir are required to save desktop_cdp screenshots")
	}
	chatDir := filepath.Join(executor.cfg.Paths.ChatsDir, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(chatDir, ".desktop-cdp-screenshot-*.tmp")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, err
	}
	referenceName := desktopCdpScreenshotReferenceName(time.Now().UTC())
	return &desktopScreenshotSink{
		file:          file,
		temporaryPath: file.Name(),
		finalPath:     filepath.Join(chatDir, referenceName),
		referenceName: referenceName,
		hash:          sha256.New(),
	}, nil
}

func (s *desktopScreenshotSink) write(decoded []byte) error {
	if s == nil || s.file == nil {
		return errors.New("desktop screenshot sink is unavailable")
	}
	if s.size+int64(len(decoded)) > desktopMaxDecodedResponseBytes {
		return errors.New("desktop screenshot exceeds 64 MiB")
	}
	if _, err := s.file.Write(decoded); err != nil {
		return err
	}
	if _, err := s.hash.Write(decoded); err != nil {
		return err
	}
	s.size += int64(len(decoded))
	return nil
}

func (s *desktopScreenshotSink) commit() (map[string]any, error) {
	if s == nil || s.file == nil || s.size == 0 {
		return nil, errors.New("desktop screenshot stream is empty")
	}
	if err := s.file.Sync(); err != nil {
		return nil, err
	}
	if err := s.file.Close(); err != nil {
		return nil, err
	}
	s.file = nil
	if err := os.Rename(s.temporaryPath, s.finalPath); err != nil {
		return nil, err
	}
	s.committed = true
	return map[string]any{
		"saved":                true,
		"dataOmitted":          true,
		"referenceName":        s.referenceName,
		"filePath":             s.finalPath,
		"mimeType":             desktopCdpScreenshotMimeType,
		"sizeBytes":            s.size,
		"sha256":               hex.EncodeToString(s.hash.Sum(nil)),
		"visionRecognizeImage": map[string]any{"reference_name": s.referenceName},
	}, nil
}

func (s *desktopScreenshotSink) abort() {
	if s == nil || s.committed {
		return
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if s.temporaryPath != "" {
		_ = os.Remove(s.temporaryPath)
	}
}

func (t *RuntimeToolExecutor) storeDesktopCdpScreenshot(result ToolExecutionResult, execCtx *ExecutionContext) ToolExecutionResult {
	payload := cloneDesktopMap(result.Structured)
	response := cloneDesktopMapValue(payload["response"])
	resultNode := cloneDesktopMapValue(response["result"])
	data, _ := resultNode["data"].(string)
	if strings.TrimSpace(data) == "" {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_data_missing", "Page.captureScreenshot response.result.data is missing", nil)
	}

	chatID := desktopCdpScreenshotChatID(execCtx)
	if strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" || !chat.ValidChatID(chatID) {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_context_unavailable", "chat context and cfg.Paths.ChatsDir are required to save desktop_cdp screenshots", map[string]any{
			"chatId":      chatID,
			"hasChatsDir": strings.TrimSpace(t.cfg.Paths.ChatsDir) != "",
		})
	}

	imageBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_decode_failed", err.Error(), nil)
	}

	referenceName := desktopCdpScreenshotReferenceName(time.Now().UTC())
	chatDir := filepath.Join(t.cfg.Paths.ChatsDir, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_write_failed", err.Error(), nil)
	}
	filePath := filepath.Join(chatDir, referenceName)
	if err := os.WriteFile(filePath, imageBytes, 0o600); err != nil {
		return desktopCdpScreenshotErrorResult(payload, "desktop_cdp_screenshot_write_failed", err.Error(), nil)
	}

	sha := sha256.Sum256(imageBytes)
	resultNode["data"] = map[string]any{
		"saved":                true,
		"dataOmitted":          true,
		"referenceName":        referenceName,
		"filePath":             filePath,
		"mimeType":             desktopCdpScreenshotMimeType,
		"sizeBytes":            len(imageBytes),
		"sha256":               hex.EncodeToString(sha[:]),
		"visionRecognizeImage": map[string]any{"reference_name": referenceName},
	}
	response["result"] = resultNode
	payload["response"] = response
	return structuredResult(payload)
}

func desktopCdpScreenshotChatID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	if chatID := strings.TrimSpace(execCtx.Request.ChatID); chatID != "" {
		return chatID
	}
	return strings.TrimSpace(execCtx.Session.ChatID)
}

func desktopCdpScreenshotReferenceName(now time.Time) string {
	return fmt.Sprintf("desktop-cdp-screenshot-%s%09dZ.png", now.Format("20060102T150405"), now.Nanosecond())
}

func desktopCdpScreenshotErrorResult(payload map[string]any, code string, message string, details map[string]any) ToolExecutionResult {
	sanitizeDesktopCdpScreenshotData(payload)
	payload["error"] = map[string]any{
		"code":    code,
		"message": message,
	}
	if details != nil {
		payload["details"] = details
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}

func sanitizeDesktopCdpScreenshotData(payload map[string]any) {
	response, ok := payload["response"].(map[string]any)
	if !ok {
		return
	}
	resultNode, ok := response["result"].(map[string]any)
	if !ok {
		return
	}
	if _, ok := resultNode["data"].(string); ok {
		resultNode["data"] = map[string]any{
			"saved":       false,
			"dataOmitted": true,
		}
	}
}

func cloneDesktopMapValue(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return cloneDesktopMap(mapped)
	}
	return map[string]any{}
}

func cloneDesktopMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		if mapped, ok := value.(map[string]any); ok {
			out[key] = cloneDesktopMap(mapped)
			continue
		}
		out[key] = value
	}
	return out
}

func buildDesktopActionSource(execCtx *ExecutionContext) (ClientRequestSource, error) {
	if execCtx == nil {
		return ClientRequestSource{}, errors.New("run execution context is required")
	}
	source := ClientRequestSource{
		RunID:  strings.TrimSpace(execCtx.Session.RunID),
		ChatID: strings.TrimSpace(execCtx.Session.ChatID),
	}
	owner := ResolveRunOwner(execCtx.Session.RunOwner)
	if owner.IsTeam() {
		source.AgentKey = ""
		source.TeamID = owner.TeamID
	} else if owner.AgentKey != "" {
		source.AgentKey = owner.AgentKey
		source.TeamID = ""
	}
	if source.RunID == "" || source.ChatID == "" {
		return ClientRequestSource{}, errors.New("runId and chatId are required for desktop actions")
	}
	if source.AgentKey != "" && source.TeamID != "" {
		return ClientRequestSource{}, errors.New("desktop action source cannot contain both agentKey and teamId")
	}
	return source, nil
}

func buildDesktopCDPSource(execCtx *ExecutionContext) desktopCDPSource {
	if execCtx == nil {
		return desktopCDPSource{}
	}
	return desktopCDPSource{
		RunID:    execCtx.Session.RunID,
		ChatID:   execCtx.Session.ChatID,
		AgentKey: execCtx.Session.AgentKey,
		TeamID:   execCtx.Session.TeamID,
	}
}

func desktopActionErrorResult(code string, message string, details map[string]any) ToolExecutionResult {
	payload := map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if details != nil {
		payload["details"] = details
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}
