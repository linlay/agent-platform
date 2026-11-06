package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/terminal"
)

// Authentication belongs to the host profile, not to a Chat. Only the owner
// sees an operation handle; neither credentials nor adapter output enter DTOs.
type authentication struct {
	state     string
	methods   []dto.AuthMethod
	operation *authOperation
}
type authOperation struct {
	id, owner string
	terminal  *terminal.Session
	cancel    context.CancelFunc
	done      chan struct{}
	ready     chan struct{}
}

func (s *Sessions) EnableAuthentication(manager *terminal.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authTerminals = manager
}
func (s *Sessions) observeAuth(engineID string, raw json.RawMessage, state string) {
	var trusted *agentengine.Lease
	if raw != nil {
		trusted, _ = s.host.registry.Acquire(engineID)
		if trusted != nil {
			defer trusted.Release()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth == nil {
		s.auth = map[string]*authentication{}
	}
	a := s.auth[engineID]
	if a == nil {
		a = &authentication{state: "unknown"}
		s.auth[engineID] = a
	}
	if raw != nil {
		var result struct {
			AuthMethods []struct {
				ID, Name, Type string
				Args           []string
			} `json:"authMethods"`
		}
		if json.Unmarshal(raw, &result) == nil {
			a.methods = []dto.AuthMethod{}
			for _, m := range result.AuthMethods {
				kind := m.Type
				if kind == "" {
					kind = "agent"
				}
				if kind != "agent" && kind != "terminal" {
					continue
				}
				available := kind == "agent" && trusted != nil && slices.Contains(trusted.AgentMethods, m.ID)
				if kind == "terminal" && trusted != nil {
					for _, allowed := range trusted.TerminalMethods {
						if allowed.MethodID == m.ID && slices.Equal(allowed.Args, m.Args) {
							available = true
						}
					}
				}
				a.methods = append(a.methods, dto.AuthMethod{ID: m.ID, Name: m.Name, Type: kind, Available: available})
			}
		}
	}
	if state != "" && a.operation == nil {
		// A first successful prompt confirms existing credentials; it does not
		// change the defaults already discovered with those credentials.
		if a.state != state && !(a.state == "unknown" && state == "authenticated") {
			s.previewEpoch++
			clear(s.previews)
		}
		a.state = state
	}
}
func (s *Sessions) authStatus(owner, engineID string) dto.AuthStatus {
	result := dto.AuthStatus{EngineID: engineID, State: "unknown", Methods: []dto.AuthMethod{}}
	if a := s.auth[engineID]; a != nil {
		result.State = a.state
		result.Methods = append(result.Methods, a.methods...)
		for i := range result.Methods {
			result.Methods[i].Available = result.Methods[i].Available && s.authTerminals != nil
		}
		if a.operation != nil && a.operation.owner == owner {
			id := a.operation.id
			result.AuthOperationID = &id
		}
	}
	return result
}
func (s *Sessions) AuthStatus(owner, engineID string) (dto.AuthStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authStatus(owner, engineID), nil
}
func (s *Sessions) StartAuthentication(owner, engineID, methodID string) (dto.AuthStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return dto.AuthStatus{}, ErrClosed
	}
	if s.authTerminals == nil || owner == "" {
		return dto.AuthStatus{}, agentengine.Error("desktop_required")
	}
	a := s.auth[engineID]
	if a == nil {
		return dto.AuthStatus{}, agentengine.Error("auth_method_unavailable")
	}
	if a.operation != nil {
		return dto.AuthStatus{}, ErrBusy
	}
	available := false
	for _, m := range a.methods {
		if m.ID == methodID && m.Available {
			available = true
		}
	}
	if !available {
		return dto.AuthStatus{}, agentengine.Error("auth_method_unavailable")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return dto.AuthStatus{}, err
	}
	ctx, cancel := context.WithTimeout(s.host.ctx, 5*time.Minute)
	op := &authOperation{id: hex.EncodeToString(token[:]), owner: owner, cancel: cancel, done: make(chan struct{}), ready: make(chan struct{})}
	a.operation = op
	a.state = "authenticating"
	s.previewEpoch++
	clear(s.previews)
	s.operations.Add(1)
	go func() {
		defer s.operations.Done()
		defer close(op.done)
		defer cancel()
		err := s.authenticate(ctx, engineID, methodID, op)
		s.mu.Lock()
		defer s.mu.Unlock()
		a.operation = nil
		a.state = "required"
		if err == nil && ctx.Err() == nil {
			a.state = "authenticated"
		}
	}()
	return s.authStatus(owner, engineID), nil
}
func (s *Sessions) CancelAuthentication(owner, operationID string) error {
	s.mu.Lock()
	for _, a := range s.auth {
		if op := a.operation; op != nil && op.id == operationID && op.owner == owner {
			op.cancel()
			s.mu.Unlock()
			<-op.done
			return nil
		}
	}
	s.mu.Unlock()
	return agentengine.Error("auth_operation_not_found")
}
func (s *Sessions) authenticate(ctx context.Context, engineID, methodID string, op *authOperation) error {
	p, err := s.host.Start(ctx, "auth-"+op.id, engineID, s.host.root, nil)
	if err != nil {
		return err
	}
	defer p.Close()
	raw, err := controlRequest(ctx, p, "initialize", s.initializeParams())
	if err != nil {
		return err
	}
	s.observeAuth(engineID, raw, "")
	s.mu.Lock()
	method := dto.AuthMethod{}
	for _, m := range s.auth[engineID].methods {
		if m.ID == methodID {
			method = m
		}
	}
	s.mu.Unlock()
	if !method.Available {
		return agentengine.Error("auth_method_unavailable")
	}
	if method.Type == "terminal" {
		err = s.authenticateTerminal(ctx, engineID, methodID, op, p.artifactDigest)
	} else {
		_, err = controlRequest(ctx, p, "authenticate", map[string]string{"methodId": methodID})
	}
	if err != nil {
		return err
	}
	if err = p.Close(); err != nil {
		return err
	}
	// A fresh process must accept the persisted profile. Opening a browser or
	// returning from authenticate alone is not evidence of a usable login.
	p, err = s.host.Start(ctx, "auth-verify-"+op.id, engineID, s.host.root, nil)
	if err != nil {
		return err
	}
	defer p.Close()
	if _, err = controlRequest(ctx, p, "initialize", s.initializeParams()); err != nil {
		return err
	}
	raw, err = controlRequest(ctx, p, "session/new", map[string]any{"cwd": s.host.root, "mcpServers": []any{}})
	if err != nil {
		return err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &result) != nil || result.SessionID == "" {
		return agentengine.Error("auth_required")
	}
	return nil
}
func (s *Sessions) initializeParams() map[string]any {
	capabilities := map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}
	s.mu.Lock()
	if s.authTerminals != nil {
		capabilities["auth"] = map[string]any{"terminal": true}
	}
	s.mu.Unlock()
	return map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "zenmind", "version": "1"}, "clientCapabilities": capabilities}
}

// AuthTerminal only attaches an already authorized helper. It cannot start a
// shell or accept a caller-supplied command, environment or working directory.
func (s *Sessions) AuthTerminal(owner, engineID, operationID string) (terminal.OpenResult, error) {
	s.mu.Lock()
	a := s.auth[engineID]
	if a == nil || a.operation == nil || a.operation.id != operationID || a.operation.owner != owner {
		s.mu.Unlock()
		return terminal.OpenResult{}, agentengine.Error("auth_operation_not_found")
	}
	op := a.operation
	s.mu.Unlock()
	// terminal.open can arrive immediately after auth/start. Wait for this exact
	// helper, never retry by launching another terminal.
	select {
	case <-op.ready:
	case <-op.done:
		return terminal.OpenResult{}, agentengine.Error("auth_operation_not_found")
	case <-time.After(10 * time.Second):
		return terminal.OpenResult{}, agentengine.Error("auth_helper_failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.operation == op && op.terminal != nil && !op.terminal.Finished() {
		return terminal.OpenResult{Session: op.terminal, Reused: true}, nil
	}
	return terminal.OpenResult{}, agentengine.Error("auth_operation_not_found")
}
func (s *Sessions) authenticateTerminal(ctx context.Context, engineID, methodID string, op *authOperation, digest string) error {
	lease, err := s.host.registry.Acquire(engineID)
	if err != nil {
		return err
	}
	defer lease.Release()
	if lease.ArtifactDigest != digest {
		return agentengine.Error("engine_unavailable")
	}
	var args []string
	found := false
	for _, m := range lease.TerminalMethods {
		if m.MethodID == methodID {
			found = true
			args = append(lease.PrefixArgs, m.Args...)
		}
	}
	if !found {
		return agentengine.Error("auth_method_unavailable")
	}
	env, err := environment(s.host.root, engineID, lease, s.host.options.Proxy)
	if err != nil {
		return err
	}
	exited := make(chan struct{})
	result, err := s.authTerminals.Open(terminal.OpenRequest{OwnerKey: op.owner, AgentKey: engineID, TerminalKey: "auth-" + op.id, Shell: lease.Command, Args: args, Env: env, CWD: s.host.root, OnExit: func() { close(exited) }})
	if err != nil {
		return agentengine.Error("auth_helper_failed")
	}
	subscription := result.Session.Subscribe(false)
	defer subscription.Close()
	defer func() { s.authTerminals.Discard(result.Session); <-exited }()
	s.mu.Lock()
	op.terminal = result.Session
	close(op.ready)
	s.mu.Unlock()
	s.authTerminals.Start(result.Session)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-subscription.Events():
			if !ok {
				return agentengine.Error("auth_helper_failed")
			}
			if event.Type == terminal.EventExit {
				if event.ExitCode != nil && *event.ExitCode == 0 {
					return nil
				}
				return agentengine.Error("auth_required")
			}
		}
	}
}
