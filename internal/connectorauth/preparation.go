package connectorauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// Preparation describes installation independently of authentication.
type Preparation struct {
	ConnectorID      string    `json:"connectorId"`
	Status           string    `json:"status"`
	Fingerprint      string    `json:"fingerprint,omitempty"`
	Stage            string    `json:"stage,omitempty"`
	Message          string    `json:"message,omitempty"`
	ExitCode         *int      `json:"exitCode,omitempty"`
	Diagnostic       string    `json:"diagnostic,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
	Executable       string    `json:"-"`
	ExecutableSHA256 string    `json:"-"`
}

// Keep executable verification data private while persisting it alongside status.
type preparationRecord struct {
	Preparation
	Executable       string `json:"executable,omitempty"`
	ExecutableSHA256 string `json:"executableSha256,omitempty"`
}
type preparationJob struct {
	state  Preparation
	cancel context.CancelFunc
	done   chan struct{}
}
type CLIExecutionError struct {
	Stage      string
	ExitCode   int
	Diagnostic string
	Cause      error
}

func (e *CLIExecutionError) Error() string {
	return fmt.Sprintf("CLI %s failed (exit %d)", e.Stage, e.ExitCode)
}
func (e *CLIExecutionError) Unwrap() error { return e.Cause }
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return -1
}

func (m *Manager) preparationPath(id string) (string, error) {
	dir, err := StateDir(m.sources.PersistentRoot(), id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "preparation.json"), nil
}
func (m *Manager) readPreparation(id string) (Preparation, error) {
	p, err := m.preparationPath(id)
	if err != nil {
		return Preparation{}, err
	}
	var record preparationRecord
	err = connector.ReadJSON(p, &record)
	record.Preparation.Executable = record.Executable
	record.Preparation.ExecutableSHA256 = record.ExecutableSHA256
	return record.Preparation, err
}
func (m *Manager) savePreparation(s Preparation) error {
	p, err := m.preparationPath(s.ConnectorID)
	if err != nil {
		return err
	}
	return savePrivateJSON(p, preparationRecord{Preparation: s, Executable: s.Executable, ExecutableSHA256: s.ExecutableSHA256})
}

// PreparationStatus never executes CLI code. An abandoned job is terminalized
// only while holding the same OS lock used by workers and package mutations.
func (m *Manager) PreparationStatus(id string) (Preparation, error) {
	pkg, err := m.sources.Load(id)
	if err != nil {
		return Preparation{}, err
	}
	if pkg.CLI == nil {
		return Preparation{}, fmt.Errorf("connector has no CLI")
	}
	m.mu.Lock()
	job := m.preparations[id]
	if job != nil {
		s := job.state
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	release, lockErr := connector.AcquireOperation(m.sources.ExternalRoot, id)
	if lockErr != nil && !errors.Is(lockErr, connector.ErrBusy) {
		return Preparation{}, lockErr
	}
	if release != nil {
		defer release()
		pkg, err = m.sources.Load(id)
		if err != nil {
			return Preparation{}, err
		}
	}
	s, err := m.readPreparation(id)
	if os.IsNotExist(err) {
		return Preparation{ConnectorID: id, Status: "pending", Message: "CLI preparation is required"}, nil
	}
	if err != nil {
		return Preparation{}, err
	}
	if lockErr == nil && (s.Status == "preparing" || s.Status == "pending") {
		s.Status = "canceled"
		s.Message = "Preparation interrupted; retry explicitly. External side effects may remain."
		s.UpdatedAt = time.Now().UTC()
		if err = m.savePreparation(s); err != nil {
			return Preparation{}, err
		}
	}
	if lockErr == nil && s.Status == "ready" {
		hash, err := connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			return Preparation{}, err
		}
		if hash != s.Fingerprint {
			s.Status = "pending"
			s.Message = "Connector changed; prepare again"
		} else if err = m.verifyPreparedEntry(pkg, s); err != nil {
			s.Status = "failed"
			s.Message = err.Error()
		}
	}
	return s, nil
}

func (m *Manager) StartPreparation(id string) (Preparation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.preparations[id]; job != nil {
		return job.state, nil
	}
	release, err := connector.AcquireOperation(m.sources.ExternalRoot, id)
	if err != nil {
		return Preparation{}, err
	}
	pkg, err := m.sources.Load(id)
	if err != nil {
		release()
		return Preparation{}, err
	}
	if pkg.CLI == nil || pkg.Builtin {
		release()
		return Preparation{}, fmt.Errorf("only external CLI connectors can be prepared")
	}
	settings, err := cliSettingsFor(pkg)
	if err != nil {
		_ = m.savePreparation(Preparation{ConnectorID: id, Status: "failed", Stage: "validation", Message: err.Error(), UpdatedAt: time.Now().UTC()})
		release()
		return Preparation{}, err
	}
	fingerprint, err := connector.RuntimeFingerprint(pkg.Dir)
	if err != nil {
		release()
		return Preparation{}, err
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	s := Preparation{ConnectorID: id, Status: "preparing", Fingerprint: fingerprint, Stage: "prepare", UpdatedAt: time.Now().UTC()}
	if err = m.savePreparation(s); err != nil {
		cancel()
		release()
		return Preparation{}, err
	}
	job := &preparationJob{state: s, cancel: cancel, done: make(chan struct{})}
	m.preparations[id] = job
	go func(s Preparation) {
		defer cancel()
		defer close(job.done)
		err := m.prepareCLI(ctx, pkg, settings)
		if err == nil {
			s.Fingerprint, err = connector.RuntimeFingerprint(pkg.Dir)
			if err == nil {
				env, envErr := m.cliEnvironment(pkg)
				err = envErr
				if err == nil {
					if pkg.BinDir != "" {
						env = hostenv.Set(env, "PATH", pkg.BinDir)
					}
					s.Executable, err = hostenv.LookPath(settings.Command, env)
					if err == nil {
						s.ExecutableSHA256, err = connector.CLIFileHash(s.Executable)
					}
				}
			}
		}
		s.UpdatedAt = time.Now().UTC()
		s.Status = "ready"
		s.Stage = "versionCheck"
		if err != nil {
			s.Stage = "prepare"
			s.Status = "failed"
			s.Message = err.Error()
			var execution *CLIExecutionError
			if errors.As(err, &execution) {
				s.Stage = execution.Stage
				s.ExitCode = &execution.ExitCode
				s.Diagnostic = execution.Diagnostic
			}
		}
		if ctx.Err() != nil {
			s.Status = "canceled"
			s.Message = "Preparation canceled or expired; external side effects may remain"
		}
		if saveErr := m.savePreparation(s); saveErr != nil {
			s.Status = "failed"
			s.Message = "Cannot persist preparation result: " + saveErr.Error()
		}
		release()
		if s.Status == "ready" && m.reload != nil {
			if reloadErr := m.reload(context.WithoutCancel(ctx), id); reloadErr != nil {
				s.Message = "CLI ready; catalog reload failed: " + reloadErr.Error()
			}
		}
		m.mu.Lock()
		job.state = s
		delete(m.preparations, id)
		m.mu.Unlock()
	}(s)
	return s, nil
}

func (m *Manager) CancelPreparation(id string) error {
	m.mu.Lock()
	job := m.preparations[id]
	if job != nil {
		job.cancel()
	}
	m.mu.Unlock()
	if job == nil {
		release, err := connector.AcquireOperation(m.sources.ExternalRoot, id)
		if err != nil {
			return err
		}
		release()
		return nil
	}
	select {
	case <-job.done:
		return nil
	case <-time.After(5 * time.Second):
		return connector.ErrBusy
	}
}
func (m *Manager) Prepare(ctx context.Context, id string) (Preparation, error) {
	if _, err := m.StartPreparation(id); err != nil {
		return Preparation{}, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		s, err := m.PreparationStatus(id)
		if err != nil {
			return s, err
		}
		if s.Status != "preparing" && s.Status != "pending" {
			if s.Status != "ready" {
				return s, fmt.Errorf("CLI preparation %s: %s", s.Status, s.Message)
			}
			return s, nil
		}
		select {
		case <-ctx.Done():
			_ = m.CancelPreparation(id)
			return s, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (m *Manager) verifyPreparedEntry(pkg connector.Package, s Preparation) error {
	settings, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	env, err := m.cliEnvironment(pkg)
	if err != nil {
		return err
	}
	if pkg.BinDir != "" {
		env = hostenv.Set(env, "PATH", pkg.BinDir)
	}
	entry, err := hostenv.LookPath(settings.Command, env)
	if err != nil {
		return err
	}
	hash, err := connector.CLIFileHash(entry)
	if err != nil || entry != s.Executable || hash != s.ExecutableSHA256 {
		return fmt.Errorf("CLI executable changed or is missing; prepare again")
	}
	return nil
}
func (m *Manager) requirePrepared(pkg connector.Package) error {
	s, err := m.readPreparation(pkg.ID)
	if err != nil {
		return fmt.Errorf("CLI is not prepared; prepare the connector first")
	}
	fingerprint, err := connector.RuntimeFingerprint(pkg.Dir)
	if err != nil {
		return err
	}
	if s.Status != "ready" || s.Fingerprint != fingerprint {
		return fmt.Errorf("CLI is not prepared for this package; prepare the connector first")
	}
	return m.verifyPreparedEntry(pkg, s)
}
