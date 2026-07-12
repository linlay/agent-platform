package kbase

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/supportpkg"
)

const (
	lanceEngineExecutableName = "kbase-lance-engine"
	lanceProtocolVersion      = 1
	lanceHandshakeTimeout     = 10 * time.Second
)

type LanceEngineHandshake struct {
	ProtocolVersion int    `json:"protocolVersion"`
	EngineVersion   string `json:"engineVersion"`
	LanceDBVersion  string `json:"lancedbVersion"`
	ListenAddress   string `json:"listenAddress"`
}

type LanceEngineState struct {
	Available       bool   `json:"available"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	EngineVersion   string `json:"engineVersion,omitempty"`
	LanceDBVersion  string `json:"lancedbVersion,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}

type LanceEngineProcess struct {
	mu      sync.Mutex
	startMu sync.Mutex

	registry  *supportpkg.Registry
	ctx       context.Context
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	client    *http.Client
	baseURL   string
	token     string
	handshake LanceEngineHandshake
	lastErr   error
	stopping  bool
}

func NewLanceEngineProcess(registry *supportpkg.Registry) *LanceEngineProcess {
	return &LanceEngineProcess{
		registry: registry,
		// Operation-specific deadlines are applied by doJSON/doArrow. A shared
		// Client.Timeout would silently cap long-running imports, index builds,
		// validation, and optimize calls at the search timeout.
		client: &http.Client{},
	}
}

func (p *LanceEngineProcess) SetRegistry(registry *supportpkg.Registry) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.registry = registry
	p.mu.Unlock()
}

func (p *LanceEngineProcess) SetLifecycleContext(ctx context.Context) {
	if p == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.ctx == nil {
		p.ctx = ctx
	}
	p.mu.Unlock()
}

func (p *LanceEngineProcess) State() LanceEngineState {
	if p == nil {
		return LanceEngineState{LastError: "lance engine process is not configured"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state := LanceEngineState{
		Available:       p.cmd != nil && p.baseURL != "" && p.lastErr == nil,
		ProtocolVersion: p.handshake.ProtocolVersion,
		EngineVersion:   p.handshake.EngineVersion,
		LanceDBVersion:  p.handshake.LanceDBVersion,
	}
	if p.lastErr != nil {
		state.LastError = p.lastErr.Error()
	}
	return state
}

func (p *LanceEngineProcess) EnsureStarted(ctx context.Context) error {
	if p == nil {
		return fmt.Errorf("lance engine process is not configured")
	}
	for attempt := 0; attempt < 4; attempt++ {
		if err := p.ensureStartedOnce(ctx); err == nil {
			return nil
		} else if attempt == 3 {
			var policyErr *PolicyError
			if errors.As(err, &policyErr) {
				return err
			}
			return &PolicyError{Kind: ErrorUnavailable, Message: "KBASE Lance engine unavailable: " + err.Error()}
		}
		delay := time.Duration(1<<attempt) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("lance engine failed to start")
}

func (p *LanceEngineProcess) ensureStartedOnce(ctx context.Context) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	p.mu.Lock()
	running := p.cmd != nil
	healthyCandidate := running && p.baseURL != "" && p.lastErr == nil
	p.mu.Unlock()
	if healthyCandidate {
		if err := p.Health(ctx); err == nil {
			return nil
		}
	}
	if running {
		p.resetProcess(true)
	}

	executable, err := p.resolveExecutable()
	if err != nil {
		p.recordError(err)
		return err
	}
	token, err := randomEngineToken()
	if err != nil {
		p.recordError(err)
		return err
	}

	p.mu.Lock()
	lifetime := p.ctx
	if lifetime == nil {
		lifetime = context.Background()
	}
	processCtx, cancel := context.WithCancel(lifetime)
	cmd := exec.CommandContext(processCtx, executable)
	cmd.Env = append(os.Environ(),
		"KBASE_LANCE_TOKEN="+token,
		"KBASE_LANCE_LISTEN_ADDR=127.0.0.1:0",
		fmt.Sprintf("KBASE_LANCE_PARENT_PID=%d", os.Getpid()),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		p.mu.Unlock()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		p.mu.Unlock()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		p.mu.Unlock()
		p.recordError(err)
		return err
	}
	p.cancel = cancel
	p.cmd = cmd
	p.token = token
	p.baseURL = ""
	p.lastErr = nil
	p.stopping = false
	p.mu.Unlock()

	go p.logStderr(stderr)
	handshake, err := readLanceHandshake(ctx, stdout)
	if err != nil {
		p.resetProcess(true)
		p.recordError(err)
		return err
	}
	if handshake.ProtocolVersion != lanceProtocolVersion {
		err := fmt.Errorf("kbase lance protocol mismatch: engine=%d runtime=%d", handshake.ProtocolVersion, lanceProtocolVersion)
		p.resetProcess(true)
		p.recordError(err)
		return err
	}
	address := strings.TrimSpace(handshake.ListenAddress)
	if address == "" {
		err := fmt.Errorf("kbase lance engine returned an empty listen address")
		p.resetProcess(true)
		p.recordError(err)
		return err
	}
	p.mu.Lock()
	p.handshake = handshake
	p.baseURL = "http://" + address
	p.lastErr = nil
	p.mu.Unlock()

	go p.wait(cmd)
	return p.Health(ctx)
}

func (p *LanceEngineProcess) resolveExecutable() (string, error) {
	if executable, err := builtins.ResolveProcessBuiltin(lanceEngineExecutableName); err == nil {
		return executable, nil
	}
	p.mu.Lock()
	registry := p.registry
	p.mu.Unlock()
	if registry != nil {
		if executable, ok := registry.Executable(lanceEngineExecutableName); ok {
			return executable.Path, nil
		}
	}
	if override := strings.TrimSpace(os.Getenv("AP_KBASE_LANCE_ENGINE")); override != "" {
		path, err := filepath.Abs(override)
		if err != nil {
			path = override
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", &PolicyError{Kind: ErrorUnavailable, Message: fmt.Sprintf("kbase lance engine executable %q is not installed for %s/%s", lanceEngineExecutableName, runtime.GOOS, runtime.GOARCH)}
}

func randomEngineToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate lance engine token: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func readLanceHandshake(ctx context.Context, reader io.Reader) (LanceEngineHandshake, error) {
	type result struct {
		handshake LanceEngineHandshake
		err       error
	}
	resultCh := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			resultCh <- result{err: fmt.Errorf("read lance engine handshake: %w", err)}
			return
		}
		var handshake LanceEngineHandshake
		if err := json.Unmarshal(scanner.Bytes(), &handshake); err != nil {
			resultCh <- result{err: fmt.Errorf("parse lance engine handshake: %w", err)}
			return
		}
		resultCh <- result{handshake: handshake}
	}()
	timer := time.NewTimer(lanceHandshakeTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return LanceEngineHandshake{}, ctx.Err()
	case <-timer.C:
		return LanceEngineHandshake{}, fmt.Errorf("lance engine handshake timed out after %s", lanceHandshakeTimeout)
	case result := <-resultCh:
		return result.handshake, result.err
	}
}

func (p *LanceEngineProcess) logStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			log.Printf("[kbase-lance] %s", line)
		}
	}
}

func (p *LanceEngineProcess) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
		p.baseURL = ""
		p.token = ""
		p.cancel = nil
		if !p.stopping && err != nil {
			p.lastErr = fmt.Errorf("kbase lance engine exited: %w", err)
		}
	}
	p.mu.Unlock()
}

func (p *LanceEngineProcess) recordError(err error) {
	p.mu.Lock()
	p.lastErr = err
	p.mu.Unlock()
}

func (p *LanceEngineProcess) Health(ctx context.Context) error {
	var response LanceEngineHandshake
	if err := p.doJSON(ctx, http.MethodGet, "/v1/health", nil, &response, 2*time.Second); err != nil {
		p.recordError(err)
		return err
	}
	if response.ProtocolVersion != lanceProtocolVersion {
		return fmt.Errorf("kbase lance health protocol mismatch: %d", response.ProtocolVersion)
	}
	p.mu.Lock()
	p.handshake = response
	p.lastErr = nil
	p.mu.Unlock()
	return nil
}

func (p *LanceEngineProcess) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	running := p.cmd != nil
	p.stopping = true
	p.mu.Unlock()
	if !running {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = p.doJSON(shutdownCtx, http.MethodPost, "/v1/shutdown", map[string]any{}, nil, 5*time.Second)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		done := p.cmd == nil
		p.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			p.stopProcess(true)
			return ctx.Err()
		case <-deadline.C:
			p.stopProcess(true)
			return nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *LanceEngineProcess) stopProcess(force bool) {
	p.mu.Lock()
	cancel := p.cancel
	cmd := p.cmd
	p.stopping = true
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if force && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *LanceEngineProcess) resetProcess(force bool) {
	p.mu.Lock()
	cancel := p.cancel
	cmd := p.cmd
	p.cmd = nil
	p.cancel = nil
	p.baseURL = ""
	p.token = ""
	p.handshake = LanceEngineHandshake{}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if force && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

type lanceErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *lanceErrorResponse) normalize() {
	if p != nil && p.Error != nil {
		if p.Code == "" {
			p.Code = p.Error.Code
		}
		if p.Message == "" {
			p.Message = p.Error.Message
		}
	}
}

type LanceEngineError struct {
	Status  int
	Code    string
	Message string
}

func (e *LanceEngineError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

func (p *LanceEngineProcess) endpoint() (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.baseURL == "" || p.token == "" || p.cmd == nil {
		if p.lastErr != nil {
			return "", "", p.lastErr
		}
		return "", "", fmt.Errorf("kbase lance engine is not running")
	}
	return p.baseURL, p.token, nil
}

func (p *LanceEngineProcess) doJSON(ctx context.Context, method, path string, request any, response any, timeout time.Duration) error {
	baseURL, token, err := p.endpoint()
	if err != nil {
		return err
	}
	var body io.Reader
	if request != nil {
		reader, writer := io.Pipe()
		go func() {
			err := json.NewEncoder(writer).Encode(request)
			_ = writer.CloseWithError(err)
		}()
		body = reader
	}
	requestCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	httpRequest, err := http.NewRequestWithContext(requestCtx, method, baseURL+path, body)
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	if request != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpResponse, err := p.client.Do(httpRequest)
	if err != nil {
		return err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var payload lanceErrorResponse
		_ = json.NewDecoder(io.LimitReader(httpResponse.Body, 1<<20)).Decode(&payload)
		payload.normalize()
		if strings.TrimSpace(payload.Message) == "" {
			payload.Message = httpResponse.Status
		}
		return &LanceEngineError{Status: httpResponse.StatusCode, Code: payload.Code, Message: payload.Message}
	}
	if response == nil || httpResponse.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, httpResponse.Body)
		return nil
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(response); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (p *LanceEngineProcess) doArrow(ctx context.Context, path string, payload []byte, headers map[string]string, response any, timeout time.Duration) error {
	baseURL, token, err := p.endpoint()
	if err != nil {
		return err
	}
	requestCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/vnd.apache.arrow.stream")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	httpResponse, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var payload lanceErrorResponse
		_ = json.NewDecoder(io.LimitReader(httpResponse.Body, 1<<20)).Decode(&payload)
		payload.normalize()
		if strings.TrimSpace(payload.Message) == "" {
			payload.Message = httpResponse.Status
		}
		return &LanceEngineError{Status: httpResponse.StatusCode, Code: payload.Code, Message: payload.Message}
	}
	if response == nil || httpResponse.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, httpResponse.Body)
		return nil
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(response); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
