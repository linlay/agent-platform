package connectorauth

import (
	"context"
	"crypto/rand"
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
// to it has a temporary private owner and contains only candidate credentials.
func (m *Manager) WithCredentialValidator(validate func(context.Context, connector.Package, map[string]string) error) *Manager {
	m.credentialValidator = validate
	return m
}

func (m *Manager) beginTokenValidation(ctx context.Context, id string) (context.Context, uint64, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disconnecting[id] || m.loggingOut[id] || m.tokenValidations[id] != nil {
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

// Candidate material never overwrites the real user's login environment. The
// installation stays shared; only credentials and HOME/XDG receive a fresh owner.
func (m *Manager) validateTokenCredentials(ctx context.Context, pkg connector.Package, values map[string]string) (string, error) {
	hasCLIStatus := pkg.CLI != nil && pkg.CLI["status"] != nil
	if !hasCLIStatus && len(pkg.MCP) == 0 {
		return "configured", nil
	}
	candidate := pkg
	candidate.Owner = "token-validation:" + rand.Text()
	root := candidate.CredentialRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", fmt.Errorf("cannot create private credential validation environment")
	}
	defer os.RemoveAll(root)
	dir, err := candidate.UserStateDir()
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

func (m *Manager) tokenStatus(ctx context.Context, pkg connector.Package) (Session, error) {
	result := Session{ConnectorID: pkg.ID, AuthBrowser: pkg.AuthorizationBrowser(), Status: "unauthorized"}
	values, ready, err := TokenValues(pkg)
	if err != nil || !ready {
		return result, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := m.validateTokenCredentials(statusCtx, pkg, values)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenRejected):
			result.Message = "Connector credentials were not accepted"
		case errors.Is(err, ErrCredentialValidatorUnavailable):
			result.Status = "setup_required"
			result.Message = "Connector credential validation is unavailable"
		default:
			result.Status = "failed"
			result.Message = "Connector credential check failed; saved credentials were preserved"
		}
		return result, nil
	}
	result.Status = status
	if status == "configured" {
		result.Message = "Credentials configured; this connector has no independent verification command"
	}
	return result, nil
}
