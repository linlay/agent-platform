// Package acp supervises trusted stdio engines. Query dispatch, session
// restoration and permission decisions remain separate Runtime responsibilities.
package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/processgroup"
	sdk "github.com/coder/acp-go-sdk"
)

var ErrBusy = errors.New("engine_busy")
var ErrClosed = errors.New("engine_connection_closed")

// Options are host policy, never decoded from plugin or public request fields.
// Defaults are conservative bounds; real engine load/recovery acceptance is T08.
type Options struct {
	MaxProcesses, MaxSessions, MaxActive, MaxQueued, MaxIncoming int
	QueueTimeout, RequestTimeout, IdleTimeout                    time.Duration
	Proxy                                                        httpclient.Config
}
type Incoming struct {
	Generation string
	ID         json.RawMessage
	Method     string
	Params     json.RawMessage
}
type Handler func(context.Context, Incoming) (any, *sdk.RequestError)

// CallError keeps pre-admission rejection distinct from an uncertain sent call.
// Error text deliberately excludes upstream payloads and process output.
type CallError struct {
	ExecutionState string
	Cause          error
}

func (e *CallError) Error() string { return "engine_call_failed: " + e.ExecutionState }
func (e *CallError) Unwrap() error { return e.Cause }

type Supervisor struct {
	registry       *agentengine.Service
	root           string
	options        Options
	mu             sync.Mutex
	processes      map[string]*Process
	closed         bool
	starting       sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	active, queued chan struct{}
}

func New(registry *agentengine.Service, root string, options Options) (*Supervisor, error) {
	if registry == nil || !filepath.IsAbs(root) {
		return nil, errors.New("invalid engine supervisor")
	}
	if options.MaxProcesses == 0 {
		options.MaxProcesses = 4
	}
	if options.MaxSessions == 0 {
		options.MaxSessions = 8
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 5 * time.Minute
	}
	if options.MaxActive == 0 {
		options.MaxActive = 2
	}
	if options.MaxQueued == 0 {
		options.MaxQueued = 8
	}
	if options.MaxIncoming == 0 {
		options.MaxIncoming = 8
	}
	if options.QueueTimeout == 0 {
		options.QueueTimeout = 5 * time.Second
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 10 * time.Minute
	}
	if options.MaxSessions < 1 || options.MaxSessions > 256 || options.IdleTimeout < 0 || options.MaxProcesses < 1 || options.MaxProcesses > 64 || options.MaxActive < 1 || options.MaxActive > 64 || options.MaxQueued < 1 || options.MaxQueued > 256 || options.MaxIncoming < 1 || options.MaxIncoming > 64 || options.QueueTimeout < 0 || options.RequestTimeout < 0 {
		return nil, errors.New("invalid engine limits")
	}
	ctx, cancel := context.WithCancel(context.Background())
	host := &Supervisor{registry: registry, root: root, options: options, processes: map[string]*Process{}, ctx: ctx, cancel: cancel, active: make(chan struct{}, options.MaxActive), queued: make(chan struct{}, options.MaxQueued)}
	registry.SetValidator(host.validateCandidate)
	return host, nil
}

// Validate only the protocol handshake; authentication and Chat creation remain
// explicit user operations. The registry keeps current published until this ends.
func (s *Supervisor) validateCandidate(engineID string, lease *agentengine.Lease) error {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	p, err := s.start(ctx, "candidate:"+engineID, engineID, s.root, nil, func() (*agentengine.Lease, error) { return lease, nil })
	if err != nil {
		lease.Release()
		return agentengine.Error("engine_unavailable")
	}
	result, callErr := controlRequest(ctx, p, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
	closeErr := p.Close()
	// A lightweight Adapter can be installed before the user installs its CLI.
	// Only an explicit dependency error defers the real handshake until Prepare.
	if closeErr == nil && errors.Is(callErr, agentengine.Error("dependency_missing")) {
		return nil
	}
	var response struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if callErr != nil || closeErr != nil || json.Unmarshal(result, &response) != nil || response.ProtocolVersion != 1 {
		return agentengine.Error("engine_unavailable")
	}
	return nil
}

func (s *Supervisor) Stats() struct{ Processes, Active, Queued int } {
	s.mu.Lock()
	defer s.mu.Unlock()
	return struct{ Processes, Active, Queued int }{len(s.processes), len(s.active), len(s.queued)}
}

// ctx owns this process's lifetime. ACP-006 supplies a Chat lifetime, not the
// short-lived HTTP preparation request. There is no automatic prompt replay.
func (s *Supervisor) Start(ctx context.Context, chatID, engineID, workspace string, handler Handler) (*Process, error) {
	return s.start(ctx, chatID, engineID, workspace, handler, func() (*agentengine.Lease, error) { return s.registry.Acquire(engineID) })
}

func (s *Supervisor) start(ctx context.Context, chatID, engineID, workspace string, handler Handler, acquire func() (*agentengine.Lease, error)) (*Process, error) {
	if chatID == "" || ctx.Err() != nil {
		return nil, errors.New("invalid engine start")
	}
	s.mu.Lock()
	_, exists := s.processes[chatID]
	if s.closed || exists || len(s.processes) >= s.options.MaxProcesses {
		s.mu.Unlock()
		return nil, ErrBusy
	}
	s.processes[chatID] = nil
	s.starting.Add(1)
	s.mu.Unlock()
	defer s.starting.Done()
	committed := false
	defer func() {
		if !committed {
			s.mu.Lock()
			delete(s.processes, chatID)
			s.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	stopClosing := context.AfterFunc(s.ctx, cancel)
	releaseContext := func() { stopClosing(); cancel() }
	defer func() {
		if !committed {
			releaseContext()
		}
	}()
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil || !filepath.IsAbs(workspace) {
		return nil, errors.New("invalid workspace")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return nil, errors.New("invalid workspace")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := acquire()
	if err != nil {
		return nil, err
	}
	defer func() {
		if !committed {
			lease.Release()
		}
	}()
	cmd := exec.Command(lease.Command, append(lease.PrefixArgs, lease.Args...)...)
	cmd.Dir = workspace
	cmd.Env, err = environment(s.root, engineID, lease, s.options.Proxy)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, childOutput, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stdout = childOutput
	stderr := &stderrCapture{}
	cmd.Stderr = stderr
	cmd.WaitDelay = 500 * time.Millisecond
	tree, err := processgroup.StartGuarded(cmd, lease.Pin)
	childOutput.Close()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	var generation [16]byte
	rand.Read(generation[:])
	p := &Process{retired: lease.Retired, pluginVersion: lease.PluginVersion, artifactDigest: lease.ArtifactDigest, stderr: stderr, generation: hex.EncodeToString(generation[:]), done: make(chan struct{}), stdin: stdin, stdout: stdout, tree: tree, host: s, prompts: map[string]bool{}, pending: make(chan struct{}, 8)}
	p.connect(handler, s.options)
	s.mu.Lock()
	s.processes[chatID] = p
	s.mu.Unlock()
	committed = true
	go func() {
		_, cleanupErr := tree.Wait()
		// os.Pipe ownership lets the protocol reader consume final buffered frames;
		// os/exec's StdoutPipe would be closed prematurely by Wait.
		select {
		case <-p.wire.done:
		case <-time.After(time.Second):
			p.stdout.Close()
		}
		p.stop()
		releaseContext()
		p.mu.Lock()
		p.cleanupErr = cleanupErr
		p.mu.Unlock()
		// If OS cleanup cannot be confirmed, pin both lease and capacity until exit.
		if cleanupErr == nil {
			lease.Release()
			s.mu.Lock()
			delete(s.processes, chatID)
			s.mu.Unlock()
		}
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.conn.Done():
			p.stop()
		case <-p.done:
		}
	}()
	return p, nil
}
func (s *Supervisor) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.starting.Wait()
	s.mu.Lock()
	all := make([]*Process, 0, len(s.processes))
	for _, p := range s.processes {
		all = append(all, p)
	}
	s.mu.Unlock()
	var result error
	for _, p := range all {
		p.stop()
	}
	for _, p := range all {
		result = errors.Join(result, p.Close())
	}
	return result
}
func (s *Supervisor) admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.active <- struct{}{}:
		return nil
	default:
	}
	select {
	case s.queued <- struct{}{}:
	default:
		return ErrBusy
	}
	defer func() { <-s.queued }()
	// Capacity wait expiry is a busy engine, not a broken connection.
	ctx, cancel := context.WithTimeoutCause(ctx, s.options.QueueTimeout, ErrBusy)
	defer cancel()
	select {
	case s.active <- struct{}{}:
		if ctx.Err() != nil {
			<-s.active
			return context.Cause(ctx)
		}
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

type Process struct {
	pluginVersion, artifactDigest string
	stderr                        *stderrCapture
	generation                    string
	host                          *Supervisor
	conn                          *sdk.Connection
	wire                          *transport
	stdin                         io.WriteCloser
	stdout                        io.ReadCloser
	tree                          *processgroup.Tree
	done                          chan struct{}
	retired                       <-chan struct{}
	once                          sync.Once
	mu                            sync.Mutex
	cleanupErr                    error
	prompts                       map[string]bool
	pending                       chan struct{}
}

func (p *Process) Generation() string    { return p.generation }
func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) stop() {
	p.once.Do(func() { p.tree.Kill(); p.stdin.Close(); p.stdout.Close(); p.wire.close() })
}
func (p *Process) Close() error {
	p.stop()
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanupErr
}
func (p *Process) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.send(ctx, method, params, false)
}

// Notifications such as session/cancel bypass prompt admission but share the
// bounded writer and deadline, so a hung stdin cannot block shutdown.
func (p *Process) Notify(ctx context.Context, method string, params any) error {
	_, err := p.send(ctx, method, params, true)
	return err
}
func (p *Process) send(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	reject := func(err error) (json.RawMessage, error) {
		return nil, &CallError{ExecutionState: "not_started", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return reject(err)
	}
	select {
	case <-p.wire.ctx.Done():
		return reject(ErrClosed)
	default:
	}
	data, err := json.Marshal(params)
	if err != nil || len(method) > 256 || method == "" {
		return reject(errors.New("invalid engine request"))
	}
	// Reject before submitting or touching the shared process. Keep the existing
	// error contract instead of misreporting a local size check as a disconnect.
	if len(data) > outgoingFrameLimit(method)-1024 {
		return reject(agentengine.Error("resource_reference_too_large"))
	}
	select {
	case p.pending <- struct{}{}:
		defer func() { <-p.pending }()
	default:
		return reject(ErrBusy)
	}
	ctx, cancel := context.WithTimeout(ctx, p.host.options.RequestTimeout)
	defer cancel()
	stopClosed := context.AfterFunc(p.wire.ctx, cancel)
	defer stopClosed()
	if !notification && method == "session/prompt" {
		var params struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(data, &params)
		p.mu.Lock()
		if p.prompts[params.SessionID] {
			p.mu.Unlock()
			return reject(ErrBusy)
		}
		p.prompts[params.SessionID] = true
		p.mu.Unlock()
		defer func() { p.mu.Lock(); delete(p.prompts, params.SessionID); p.mu.Unlock() }()
		if err = p.host.admit(ctx); err != nil {
			return reject(err)
		}
		defer func() { <-p.host.active }()
	}
	if err = ctx.Err(); err != nil {
		return reject(err)
	}
	if !notification && method == "session/prompt" {
		return p.sendPrompt(ctx, data)
	}
	stopTimeout := context.AfterFunc(ctx, p.stop)
	defer stopTimeout()
	var result json.RawMessage
	if notification {
		err = p.conn.SendNotification(ctx, method, json.RawMessage(data))
	} else {
		result, err = sdk.SendRequest[json.RawMessage](p.conn, ctx, method, json.RawMessage(data))
	}
	if ctx.Err() != nil {
		p.stop()
		return nil, &CallError{ExecutionState: "unknown", Cause: ctx.Err()}
	}
	if err != nil {
		return nil, &CallError{ExecutionState: "unknown", Cause: err}
	}
	return result, nil
}

// Admission uses the caller context. Once sent, cancellation must wait for
// the upstream prompt to finish before the session can be reused.
func (p *Process) sendPrompt(ctx context.Context, data json.RawMessage) (json.RawMessage, error) {
	sent, done := make(chan struct{}), make(chan struct{})
	var params struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(data, &params)
	p.wire.writeMu.Lock()
	p.wire.promptWritten[params.SessionID] = sent
	p.wire.writeMu.Unlock()
	requestCtx, cancelRequest := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequest()
	stopClosed := context.AfterFunc(p.wire.ctx, cancelRequest)
	defer stopClosed()
	cancelled := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelled)
		grace, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopGrace := context.AfterFunc(grace, p.stop)
		defer stopGrace()
		select {
		case <-sent:
		case <-done:
			return
		case <-grace.Done():
			p.stop()
			return
		}
		var params struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(data, &params)
		if err := p.Notify(grace, "session/cancel", params); err != nil {
			p.stop()
			return
		}
		select {
		case <-done:
		case <-grace.Done():
			p.stop()
		}
	})
	result, err := sdk.SendRequest[json.RawMessage](p.conn, requestCtx, "session/prompt", data)
	close(done)
	if !stopCancel() {
		<-cancelled
	}
	p.wire.writeMu.Lock()
	delete(p.wire.promptWritten, params.SessionID)
	p.wire.writeMu.Unlock()
	if err != nil {
		return nil, &CallError{ExecutionState: "unknown", Cause: err}
	}
	return result, nil
}

// Keep a bounded private diagnostic tail. Public status intentionally exposes
// counts only: CLI stderr may contain credentials or authentication URLs.
type stderrCapture struct {
	mu    sync.Mutex
	tail  []byte
	total int64
}

func (s *stderrCapture) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(data)
	s.total += int64(n)
	const limit = 64 * 1024
	if n >= limit {
		s.tail = append(s.tail[:0], data[n-limit:]...)
	} else {
		if extra := len(s.tail) + n - limit; extra > 0 {
			s.tail = append(s.tail[:0], s.tail[extra:]...)
		}
		s.tail = append(s.tail, data...)
	}
	return n, nil
}
func (p *Process) Diagnostics() struct {
	StderrBytes    int64
	StderrRetained int
} {
	p.stderr.mu.Lock()
	defer p.stderr.mu.Unlock()
	return struct {
		StderrBytes    int64
		StderrRetained int
	}{p.stderr.total, len(p.stderr.tail)}
}
