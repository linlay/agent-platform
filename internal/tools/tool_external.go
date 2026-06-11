package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

const (
	defaultExternalToolStartupTimeout = 5
	defaultExternalToolTimeout        = 30
)

type ExternalToolInvoker interface {
	Configure(defs []api.ToolDetailResponse)
	Invoke(ctx context.Context, def api.ToolDetailResponse, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error)
	Close() error
}

type ExternalToolManager struct {
	mu       sync.RWMutex
	services map[string]*externalToolService
}

func NewExternalToolManager() *ExternalToolManager {
	return &ExternalToolManager{services: map[string]*externalToolService{}}
}

func (m *ExternalToolManager) Configure(defs []api.ToolDetailResponse) {
	if m == nil {
		return
	}
	next := map[string]*externalToolService{}
	for _, def := range defs {
		cfg, ok, err := externalServiceConfigFromTool(def)
		if err != nil {
			log.Printf("[tools][external] skip tool %q: %v", def.Name, err)
			continue
		}
		if !ok {
			continue
		}
		if _, exists := next[cfg.ServiceKey]; exists {
			continue
		}
		next[cfg.ServiceKey] = newExternalToolService(cfg)
	}

	m.mu.Lock()
	previous := m.services
	m.services = next
	m.mu.Unlock()

	for _, service := range previous {
		_ = service.close()
	}
	for _, service := range next {
		startCtx, cancel := context.WithTimeout(context.Background(), time.Duration(service.cfg.StartupTimeout)*time.Second)
		if err := service.ensureStarted(startCtx); err != nil {
			log.Printf("[tools][external] service %q unavailable: %v", service.cfg.ServiceKey, err)
		}
		cancel()
	}
}

func (m *ExternalToolManager) Invoke(ctx context.Context, def api.ToolDetailResponse, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	cfg, ok, err := externalServiceConfigFromTool(def)
	if err != nil {
		return externalToolError(def.Name, "external_tool_invalid_config", err.Error()), nil
	}
	if !ok {
		return externalToolError(def.Name, "external_tool_not_configured", "external tool metadata is missing"), nil
	}
	m.mu.RLock()
	service := m.services[cfg.ServiceKey]
	m.mu.RUnlock()
	if service == nil {
		return externalToolError(def.Name, "external_tool_unavailable", "external service is not configured: "+cfg.ServiceKey), nil
	}
	timeout := time.Duration(service.cfg.Timeout) * time.Second
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	payload, err := service.callTool(ctx, def.Name, args, buildMCPMeta(def.Name, execCtx))
	if err != nil {
		code := "external_tool_call_failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			code = "external_tool_timeout"
		} else if errors.Is(err, errExternalServiceUnavailable) {
			code = "external_tool_unavailable"
		}
		return externalToolError(def.Name, code, err.Error()), nil
	}
	return externalPayloadToToolResult(def.Name, payload), nil
}

func (m *ExternalToolManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	services := m.services
	m.services = map[string]*externalToolService{}
	m.mu.Unlock()
	var errs []string
	for _, service := range services {
		if err := service.close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type externalServiceConfig struct {
	ServiceKey     string
	Command        string
	Args           []string
	Env            map[string]string
	WorkingDir     string
	StartupTimeout int
	Timeout        int
}

func externalServiceConfigFromTool(def api.ToolDetailResponse) (externalServiceConfig, bool, error) {
	kind, _ := def.Meta["kind"].(string)
	if !strings.EqualFold(strings.TrimSpace(kind), "external") {
		return externalServiceConfig{}, false, nil
	}
	raw := AnyMapNode(def.Meta["external"])
	if len(raw) == 0 {
		return externalServiceConfig{}, true, fmt.Errorf("external metadata is missing")
	}
	transport := AnyStringNode(raw["transport"])
	if transport == "" {
		transport = "stdio-jsonrpc"
	}
	if !strings.EqualFold(transport, "stdio-jsonrpc") {
		return externalServiceConfig{}, true, fmt.Errorf("unsupported external transport %q", transport)
	}
	serviceKey := strings.TrimSpace(AnyStringNode(raw["serviceKey"]))
	if serviceKey == "" {
		serviceKey = strings.TrimSpace(def.Name)
	}
	command := strings.TrimSpace(AnyStringNode(raw["command"]))
	if command == "" {
		return externalServiceConfig{}, true, fmt.Errorf("external.command is required")
	}
	startupTimeout := AnyIntNode(raw["startupTimeout"])
	if startupTimeout <= 0 {
		startupTimeout = defaultExternalToolStartupTimeout
	}
	timeout := AnyIntNode(raw["timeout"])
	if timeout <= 0 {
		timeout = AnyIntNode(def.Meta["timeout"])
	}
	if timeout <= 0 {
		timeout = defaultExternalToolTimeout
	}
	return externalServiceConfig{
		ServiceKey:     serviceKey,
		Command:        command,
		Args:           anyStringSlice(raw["args"]),
		Env:            anyStringMap(raw["env"]),
		WorkingDir:     strings.TrimSpace(AnyStringNode(raw["workingDirectory"])),
		StartupTimeout: startupTimeout,
		Timeout:        timeout,
	}, true, nil
}

func anyStringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func anyStringMap(value any) map[string]string {
	raw, ok := value.(map[string]any)
	if !ok || len(raw) == 0 {
		if typed, ok := value.(map[string]string); ok {
			return CloneStringMap(typed)
		}
		return nil
	}
	out := map[string]string{}
	for key, value := range raw {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = AnyStringNode(value)
	}
	return out
}

func defaultExternalArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

var errExternalServiceUnavailable = errors.New("external service unavailable")

type externalToolService struct {
	cfg externalServiceConfig

	startMu sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	lastErr error

	nextID atomic.Int64

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan externalRPCResponse

	closeMu  sync.Mutex
	isClosed bool
}

func newExternalToolService(cfg externalServiceConfig) *externalToolService {
	return &externalToolService{
		cfg:     cfg,
		pending: map[string]chan externalRPCResponse{},
	}
}

func (s *externalToolService) callTool(ctx context.Context, toolName string, args map[string]any, meta map[string]any) (any, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", errExternalServiceUnavailable, err)
	}
	params := map[string]any{
		"name":      toolName,
		"arguments": defaultExternalArgs(args),
	}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	return s.request(ctx, "tools/call", params)
}

func (s *externalToolService) ensureStarted(ctx context.Context) error {
	if s == nil {
		return errExternalServiceUnavailable
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.isRunningLocked() {
		return nil
	}
	if s.isClosed {
		return errExternalServiceUnavailable
	}
	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	if strings.TrimSpace(s.cfg.WorkingDir) != "" {
		cmd.Dir = s.cfg.WorkingDir
	}
	cmd.Env = append(os.Environ(), stringMapEnv(s.cfg.Env)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.lastErr = err
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.lastErr = err
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.lastErr = err
		return err
	}
	if err := cmd.Start(); err != nil {
		s.lastErr = err
		return err
	}
	s.cmd = cmd
	s.stdin = stdin
	s.done = make(chan struct{})
	s.lastErr = nil
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop(cmd)
	if _, err := s.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2026-06",
		"clientInfo": map[string]any{
			"name": "agent-platform",
		},
	}); err != nil {
		s.lastErr = err
		_ = s.killLocked()
		return err
	}
	return nil
}

func (s *externalToolService) isRunningLocked() bool {
	if s.cmd == nil || s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func stringMapEnv(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "=\x00") {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

func (s *externalToolService) request(ctx context.Context, method string, params map[string]any) (any, error) {
	id := fmt.Sprintf("%d", s.nextID.Add(1))
	respCh := make(chan externalRPCResponse, 1)
	s.pendingMu.Lock()
	s.pending[id] = respCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
	}()

	body, err := json.Marshal(externalRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	if s.stdin == nil {
		s.writeMu.Unlock()
		return nil, errExternalServiceUnavailable
	}
	_, err = s.stdin.Write(append(body, '\n'))
	s.writeMu.Unlock()
	if err != nil {
		return nil, err
	}

	done := s.done
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("jsonrpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-done:
		if s.lastErr != nil {
			return nil, fmt.Errorf("%w: %v", errExternalServiceUnavailable, s.lastErr)
		}
		return nil, errExternalServiceUnavailable
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *externalToolService) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp externalRPCResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			log.Printf("[tools][external] service %q returned invalid JSON: %v", s.cfg.ServiceKey, err)
			continue
		}
		if strings.TrimSpace(resp.ID) == "" {
			continue
		}
		s.pendingMu.Lock()
		ch := s.pending[resp.ID]
		s.pendingMu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	if err := scanner.Err(); err != nil {
		s.lastErr = err
	}
}

func (s *externalToolService) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			log.Printf("[tools][external][%s] %s", s.cfg.ServiceKey, line)
		}
	}
}

func (s *externalToolService) waitLoop(cmd *exec.Cmd) {
	err := cmd.Wait()
	if err != nil && s.lastErr == nil {
		s.lastErr = err
	}
	s.pendingMu.Lock()
	for id := range s.pending {
		delete(s.pending, id)
	}
	s.pendingMu.Unlock()
	if s.done != nil {
		close(s.done)
	}
}

func (s *externalToolService) close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	if s.isClosed {
		s.closeMu.Unlock()
		return nil
	}
	s.isClosed = true
	s.closeMu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if s.isRunning() {
		_, _ = s.request(shutdownCtx, "shutdown", nil)
	}
	cancel()

	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	return s.killLocked()
}

func (s *externalToolService) isRunning() bool {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	return s.isRunningLocked()
}

func (s *externalToolService) killLocked() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

type externalRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type externalRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      string            `json:"id"`
	Result  any               `json:"result,omitempty"`
	Error   *externalRPCError `json:"error,omitempty"`
}

type externalRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func externalPayloadToToolResult(toolName string, payload any) ToolExecutionResult {
	if payload == nil {
		return externalToolError(toolName, "external_tool_empty_result", "external tool returned an empty result")
	}
	switch value := payload.(type) {
	case map[string]any:
		if code := strings.TrimSpace(AnyStringNode(value["error"])); code != "" {
			message := strings.TrimSpace(AnyStringNode(value["message"]))
			if message == "" {
				message = code
			}
			result := structuredResultWithExit(value, -1)
			result.Error = code
			result.Output = MarshalJSON(value)
			if strings.TrimSpace(result.Output) == "" {
				result.Output = message
			}
			return result
		}
		return structuredResult(value)
	case string:
		return ToolExecutionResult{Output: value, ExitCode: 0}
	default:
		return ToolExecutionResult{Output: MarshalJSON(value), ExitCode: 0}
	}
}

func externalToolError(toolName string, code string, message string) ToolExecutionResult {
	payload := map[string]any{
		"tool":    toolName,
		"ok":      false,
		"code":    code,
		"error":   code,
		"message": message,
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}
