package webapphost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
)

type BridgeGrant struct {
	URL, Token string
	Revoke     func()
}
type BridgeFactory func(webapp.RuntimeSpec) (BridgeGrant, error)
type Runner struct {
	config config.WebAppsConfig
	root   string
	ready  bool
	mu     sync.Mutex
	ports  map[int]bool
	bridge BridgeFactory
}

func NewRunner(root string, cfg config.WebAppsConfig, ready bool) *Runner {
	return &Runner{root: root, config: cfg, ready: ready, ports: map[int]bool{}}
}
func (r *Runner) SetBridge(factory BridgeFactory) { r.mu.Lock(); r.bridge = factory; r.mu.Unlock() }
func (r *Runner) Ready() bool                     { return r.ready }
func runtimeError(code string) error              { return &webapp.Error{Code: code, Status: 503} }
func (r *Runner) reservePort() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for port := r.config.Backend.PortStart; port <= r.config.Backend.PortEnd; port++ {
		if r.ports[port] {
			continue
		}
		l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		l.Close()
		r.ports[port] = true
		return port, nil
	}
	return 0, runtimeError("webapp_backend_ports_exhausted")
}
func (r *Runner) releasePort(port int) { r.mu.Lock(); delete(r.ports, port); r.mu.Unlock() }

type supervisorStatus struct {
	Event string `json:"event"`
	Stage string `json:"stage"`
	PID   int    `json:"pid"`
	Code  int    `json:"code"`
	Clean bool   `json:"clean"`
}
type process struct {
	address       string
	command       *exec.Cmd
	control       *os.File
	done          chan struct{}
	stopOnce      sync.Once
	mu            sync.Mutex
	result        webapp.ProcessExit
	status        supervisorStatus
	nodePID       int
	reason        string
	onUnavailable func(string)
	lease         string
	logs          []*rotationWriter
}

func (p *process) Address() string          { return p.address }
func (p *process) Done() <-chan struct{}    { return p.done }
func (p *process) Exit() webapp.ProcessExit { p.mu.Lock(); defer p.mu.Unlock(); return p.result }
func (p *process) requestStop(reason string) {
	if reason != "" && p.onUnavailable != nil {
		p.onUnavailable(reason)
	}
	p.mu.Lock()
	if p.reason == "" {
		p.reason = reason
	}
	p.mu.Unlock()
	p.stopOnce.Do(func() { p.control.Close() })
}
func (p *process) Stop(ctx context.Context) error {
	p.requestStop("")
	select {
	case <-p.done:
		if !p.Exit().Clean {
			return runtimeError("webapp_stop_incomplete")
		}
		return nil
	case <-ctx.Done():
		return runtimeError("webapp_stop_incomplete")
	}
}
func (r *Runner) Start(ctx context.Context, spec webapp.RuntimeSpec) (webapp.Process, error) {
	if !r.ready || spec.Manifest.Backend == nil {
		return nil, runtimeError("webapp_backend_unavailable")
	}
	if err := verifyExecutable(NodePath, "/opt/node/node.sha256"); err != nil {
		return nil, runtimeError("webapp_backend_unavailable")
	}
	if err := verifyExecutable(SupervisorPath, "/opt/libexec/webapp-supervisor.sha256"); err != nil {
		return nil, runtimeError("webapp_backend_unavailable")
	}
	leasePath := filepath.Join(spec.ManagementRoot, "generation.lock")
	lease, err := lockGeneration(leasePath)
	if err != nil {
		return nil, runtimeError("webapp_previous_generation_alive")
	}
	defer lease.Close()
	port, err := r.reservePort()
	if err != nil {
		return nil, err
	}
	keepPort := false
	defer func() {
		if !keepPort {
			r.releasePort(port)
		}
	}()
	tmp := filepath.Join(spec.ManagementRoot, ".generations", spec.Generation)
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return nil, runtimeError("webapp_temporary_directory_failed")
	}
	if err := os.MkdirAll(spec.DataRoot, 0700); err != nil {
		return nil, runtimeError("webapp_data_directory_failed")
	}
	for _, dir := range []string{tmp, spec.CodeRoot, spec.DataRoot, spec.ManagementRoot} {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || real != dir {
			return nil, runtimeError("webapp_runtime_path_invalid")
		}
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			os.RemoveAll(tmp)
		}
	}()
	grant := BridgeGrant{Revoke: func() {}}
	r.mu.Lock()
	bridge := r.bridge
	r.mu.Unlock()
	if bridge != nil {
		grant, err = bridge(spec)
		if err != nil {
			return nil, err
		}
		if grant.Revoke == nil {
			grant.Revoke = func() {}
		}
	}
	keepGrant := false
	defer func() {
		if !keepGrant {
			grant.Revoke()
		}
	}()
	bridgePort := 0
	if grant.URL != "" {
		host := strings.TrimPrefix(grant.URL, "http://")
		host = strings.SplitN(host, "/", 2)[0]
		_, rawPort, err := net.SplitHostPort(host)
		if err != nil || !strings.HasPrefix(host, "127.0.0.1:") {
			return nil, runtimeError("webapp_bridge_unavailable")
		}
		bridgePort, _ = strconv.Atoi(rawPort)
		if bridgePort < 1024 {
			return nil, runtimeError("webapp_bridge_unavailable")
		}
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer controlRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		controlWrite.Close()
		return nil, err
	}
	defer statusWrite.Close()
	configFile := filepath.Join(spec.ManagementRoot, ".generations", spec.Generation+".json")
	configJSON, err := json.Marshal(spec.UserConfig)
	if err != nil {
		return nil, runtimeError("webapp_user_config_invalid")
	}
	if err := os.WriteFile(configFile, configJSON, 0400); err != nil {
		return nil, runtimeError("webapp_temporary_directory_failed")
	}
	keepConfig := false
	defer func() {
		if !keepConfig {
			os.Remove(configFile)
		}
	}()
	b := spec.Manifest.Backend
	ports := make([]string, 0, len(spec.Policy.ConnectPorts))
	for _, port := range spec.Policy.ConnectPorts {
		ports = append(ports, strconv.Itoa(port))
	}
	args := []string{"--run", spec.CodeRoot, spec.DataRoot, tmp, configFile, strconv.Itoa(port), strconv.Itoa(bridgePort), strings.Join(ports, ","), strconv.Itoa(b.ShutdownTimeoutMS), "--", NodePath,
		"--permission", "--no-addons", "--max-old-space-size=" + strconv.Itoa(r.config.Backend.MaxOldSpaceSizeMB), "--allow-fs-read=" + spec.CodeRoot, "--allow-fs-read=" + spec.DataRoot, "--allow-fs-read=" + tmp, "--allow-fs-read=" + configFile, "--allow-fs-write=" + spec.DataRoot, "--allow-fs-write=" + tmp, filepath.Join(spec.CodeRoot, filepath.FromSlash(b.Command.Entry))}
	args = append(args, b.Args...)
	cmd := exec.Command(SupervisorPath, args...)
	cmd.Dir = spec.CodeRoot
	env := map[string]string{"PATH": "/opt/node/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TZ": "UTC", "HOST": "127.0.0.1", "PORT": strconv.Itoa(port), "HOME": tmp, "TMPDIR": tmp, "TMP": tmp, "TEMP": tmp, "NODE_ENV": "production", "WEBAPP_ID": spec.ID, "WEBAPP_KEY": spec.Manifest.Key, "WEBAPP_DIR": spec.CodeRoot, "WEBAPP_DATA_DIR": spec.DataRoot, "WEBAPP_USER_CONFIG_FILE": configFile}
	for key, value := range b.Env {
		env[key] = value
	}
	// Reserved values are set after the package's non-reserved environment.
	env["HOST"] = "127.0.0.1"
	env["PORT"] = strconv.Itoa(port)
	env["HOME"] = tmp
	env["TMPDIR"] = tmp
	env["TMP"] = tmp
	env["TEMP"] = tmp
	if grant.URL != "" {
		env["DESKTOP_ACTION_BRIDGE_URL"] = grant.URL
		env["DESKTOP_ACTION_BRIDGE_TOKEN"] = grant.Token
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cmd.Env = append(cmd.Env, key+"="+env[key])
	}
	cmd.ExtraFiles = []*os.File{controlRead, statusWrite, lease}
	out, err := newRotationWriter(filepath.Join(spec.ManagementRoot, "logs"), "stdout", r.config.Logs)
	if err != nil {
		controlWrite.Close()
		statusRead.Close()
		return nil, runtimeError("webapp_logs_unavailable")
	}
	stderr, err := newRotationWriter(filepath.Join(spec.ManagementRoot, "logs"), "stderr", r.config.Logs)
	if err != nil {
		out.Close()
		controlWrite.Close()
		statusRead.Close()
		return nil, runtimeError("webapp_logs_unavailable")
	}
	cmd.Stdout = out
	cmd.Stderr = stderr
	p := &process{onUnavailable: spec.OnUnavailable, address: "127.0.0.1:" + strconv.Itoa(port), command: cmd, control: controlWrite, done: make(chan struct{}), lease: leasePath, logs: []*rotationWriter{out, stderr}}
	if err := cmd.Start(); err != nil {
		out.Close()
		stderr.Close()
		statusRead.Close()
		controlWrite.Close()
		return nil, runtimeError("webapp_supervisor_start_failed")
	}
	controlRead.Close()
	statusWrite.Close()
	lease.Close()
	keepPort = true
	keepTmp = true
	keepGrant = true
	keepConfig = true
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		defer statusRead.Close()
		scanner := bufio.NewScanner(statusRead)
		scanner.Buffer(make([]byte, 1024), 4096)
		for scanner.Scan() {
			var state supervisorStatus
			if json.Unmarshal(scanner.Bytes(), &state) != nil {
				p.requestStop("webapp_supervisor_protocol_failed")
				return
			}
			p.mu.Lock()
			p.status = state
			if state.Event == "started" {
				p.nodePID = state.PID
			}
			p.mu.Unlock()
		}
	}()
	go func() {
		waitErr := cmd.Wait()
		<-statusDone
		p.control.Close()
		grant.Revoke()
		for _, log := range p.logs {
			log.Close()
		}
		p.mu.Lock()
		state := p.status
		reason := p.reason
		p.mu.Unlock()
		clean := state.Event == "exited" && state.Clean
		// If the outer watcher was killed, its inner subreaper retains the lease
		// until every descendant exits. A new generation must acquire that lock.
		if !clean {
			deadline := time.Now().Add(time.Duration(b.ShutdownTimeoutMS+3000) * time.Millisecond)
			for time.Now().Before(deadline) {
				claim, err := lockGeneration(leasePath)
				if err == nil {
					claim.Close()
					clean = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		code := 0
		if waitErr != nil {
			var exit *exec.ExitError
			if errors.As(waitErr, &exit) {
				code = exit.ExitCode()
			} else {
				code = -1
			}
		}
		if reason == "" {
			reason = "webapp_backend_exited"
		}
		if code == 77 {
			reason = "webapp_isolation_required"
		}
		if !clean {
			reason = "webapp_stop_incomplete"
		}
		p.mu.Lock()
		p.result = webapp.ProcessExit{Code: code, Clean: clean, Reason: reason}
		p.mu.Unlock()
		if clean {
			r.releasePort(port)
			os.RemoveAll(tmp)
			os.Remove(configFile)
		}
		close(p.done)
	}()
	startup, cancel := context.WithTimeout(ctx, time.Duration(b.Health.StartupTimeoutMS)*time.Millisecond)
	defer cancel()
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := p.healthy(startup, b); err == nil {
			go p.monitor(ctx, b, r.config.Backend)
			return p, nil
		}
		select {
		case <-startup.Done():
			p.requestStop("webapp_startup_timeout")
			stopCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			err := p.Stop(stopCtx)
			cancel()
			if err != nil {
				return nil, err
			}
			return nil, runtimeError("webapp_startup_timeout")
		case <-p.done:
			return nil, runtimeError(p.Exit().Reason)
		case <-timer.C:
		}
	}
}
func (p *process) healthy(ctx context.Context, b *webapp.Backend) error {
	p.mu.Lock()
	pid := p.nodePID
	p.mu.Unlock()
	if pid <= 0 || !ownsLoopbackPort(pid, p.address) {
		return fmt.Errorf("not listening")
	}
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if b.Health.Type == "tcp" {
		connection, err := (&net.Dialer{}).DialContext(check, "tcp4", p.address)
		if err == nil {
			connection.Close()
		}
		return err
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(check, http.MethodGet, "http://"+p.address+b.Health.Path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("unhealthy")
	}
	return nil
}
func (p *process) monitor(ctx context.Context, b *webapp.Backend, cfg config.WebAppBackendConfig) {
	ticker := time.NewTicker(time.Duration(cfg.HealthIntervalSeconds) * time.Second)
	defer ticker.Stop()
	unhealthy := 0
	for {
		select {
		case <-p.done:
			return
		case <-ctx.Done():
			p.requestStop("")
			return
		case <-ticker.C:
			if processTreeRSS(p.command.Process.Pid) > uint64(cfg.RSSStopMB)<<20 {
				p.requestStop("webapp_rss_threshold_exceeded")
				return
			}
			for _, log := range p.logs {
				if log.Failed() {
					p.requestStop("webapp_logs_unavailable")
					return
				}
			}
			if p.healthy(ctx, b) != nil {
				unhealthy++
			} else {
				unhealthy = 0
			}
			if unhealthy >= cfg.UnhealthyThreshold {
				p.requestStop("webapp_health_failed")
				return
			}
		}
	}
}

func (r *Runner) ConfirmStopped(id string) bool {
	if len(id) != 23 || !strings.HasPrefix(id, "webapp-") {
		return false
	}
	for _, c := range strings.TrimPrefix(id, "webapp-") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	file, err := lockGeneration(filepath.Join(r.root, id, "generation.lock"))
	if err != nil {
		return false
	}
	file.Close()
	return true
}
