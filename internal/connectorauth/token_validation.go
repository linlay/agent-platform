package connectorauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-platform/internal/connector"
)

// ErrTokenRejected is reserved for a component's explicit authentication failure.
// Network and setup failures must never be misclassified as expired credentials.
var ErrTokenRejected = errors.New("connector credentials were not accepted")
var ErrCredentialValidatorUnavailable = errors.New("connector credential validation is unavailable")
var ErrCredentialCheckFailed = errors.New("connector credential check failed; saved credentials were preserved")

type tokenValidation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// WithCredentialValidator installs a trusted component probe. The package passed
// to it has a temporary private state root and contains only candidate credentials.
func (m *Manager) WithCredentialValidator(validate func(context.Context, connector.Package, map[string]string) error) *Manager {
	m.credentialValidator = validate
	return m
}

func (m *Manager) beginTokenValidation(ctx context.Context, id string) (context.Context, uint64, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disconnecting[id] || m.tokenValidations[id] != nil {
		return nil, 0, nil, fmt.Errorf("connector credential operation is in progress")
	}
	validationCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	job := &tokenValidation{cancel: cancel, done: make(chan struct{})}
	if m.tokenValidations == nil {
		m.tokenValidations = map[string]*tokenValidation{}
	}
	m.tokenValidations[id] = job
	return validationCtx, m.epochs[id], func() {
		cancel()
		m.mu.Lock()
		if m.tokenValidations[id] == job {
			delete(m.tokenValidations, id)
		}
		close(job.done)
		m.mu.Unlock()
	}, nil
}

// Candidate credentials never overwrite the active connection. The prepared
// installation is reused, while credentials and HOME/XDG use a temporary root.
func (m *Manager) validateTokenCredentials(ctx context.Context, pkg connector.Package, values map[string]string) (string, error) {
	hasCLIStatus := pkg.CLI != nil && pkg.CLI["status"] != nil
	if !hasCLIStatus && len(pkg.MCP) == 0 {
		return "configured", nil
	}
	root, err := os.MkdirTemp("", "connector-credential-validation-")
	if err != nil {
		return "", fmt.Errorf("cannot create private credential validation environment")
	}
	defer os.RemoveAll(root)
	candidate := pkg
	candidate.StateRoot = root
	dir, err := candidate.ConnectorStateDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"home", "config", "cache", "data", "state", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return "", fmt.Errorf("cannot prepare private credential validation environment")
		}
	}
	path, err := connector.CredentialsPath(candidate.CredentialRoot(), candidate.ID)
	if err != nil {
		return "", err
	}
	if err = savePrivateJSON(path, values); err != nil {
		return "", fmt.Errorf("cannot stage connector credentials")
	}
	if _, err := candidate.SetConfigured(true); err != nil {
		return "", ErrCredentialCheckFailed
	}
	if hasCLIStatus {
		accepted, err := m.cliStatus(ctx, candidate)
		if err != nil {
			return "", ErrCredentialCheckFailed
		}
		if !accepted {
			return "", ErrTokenRejected
		}
	}
	if len(pkg.MCP) > 0 {
		if m.credentialValidator == nil {
			return "", ErrCredentialValidatorUnavailable
		}
		if err := m.credentialValidator(ctx, candidate, values); err != nil {
			if errors.Is(err, ErrTokenRejected) {
				return "", ErrTokenRejected
			}
			if errors.Is(err, ErrCredentialValidatorUnavailable) {
				return "", ErrCredentialValidatorUnavailable
			}
			return "", ErrCredentialCheckFailed
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "authorized", nil
}

// tokenStatus is a local snapshot, never a network probe.
func (m *Manager) tokenStatus(ctx context.Context, pkg connector.Package) (Session, error) {
	result := Session{ConnectorID: pkg.ID, AuthBrowser: pkg.AuthorizationBrowser(), Status: "unauthorized"}
	_, pending, err := pendingTokenValues(pkg)
	if err != nil {
		return result, err
	}
	result.PendingVerification = pending
	values, ready, err := TokenValues(pkg)
	if err != nil {
		return result, err
	}
	if !ready {
		if pending {
			result.Status = "pending_verification"
		}
		return result, nil
	}
	result.Status = cachedVerification(pkg, values)
	if result.Status == "" {
		result.Status = "configured"
	}
	return result, nil
}

func (m *Manager) cancelTokenValidation(ctx context.Context, id string) error {
	m.mu.Lock()
	job := m.tokenValidations[id]
	if job != nil {
		job.cancel()
	}
	m.mu.Unlock()
	if job != nil {
		select {
		case <-job.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
