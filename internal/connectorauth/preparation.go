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

// Preparation describes the latest installation attempt independently of
// authentication. The CLI in use is the active installation, which a failed
// attempt never replaces; ActiveVersion reports it when it is an older one.
type Preparation struct {
	ConnectorID       string    `json:"connectorId"`
	Status            string    `json:"status"`
	Fingerprint       string    `json:"fingerprint,omitempty"`
	Stage             string    `json:"stage,omitempty"`
	Message           string    `json:"message,omitempty"`
	ExitCode          *int      `json:"exitCode,omitempty"`
	Diagnostic        string    `json:"diagnostic,omitempty"`
	ActiveVersion     string    `json:"activeVersion,omitempty"`
	ActiveFingerprint string    `json:"activeFingerprint,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt"`
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
	dir, err := connector.InstallDir(m.sources.InstallRoot(), id)
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
	var s Preparation
	err = connector.ReadJSON(p, &s)
	return s, err
}
func (m *Manager) savePreparation(s Preparation) error {
	p, err := m.preparationPath(s.ConnectorID)
	if err != nil {
		return err
	}
	// Preparation is program state, not a credential: it no longer lives in .state.
	if dir, err := StateDir(m.sources.PersistentRoot(), s.ConnectorID); err == nil {
		_ = os.Remove(filepath.Join(dir, "preparation.json"))
	}
	return savePrivateJSON(p, s)
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
	installation, activeErr := connector.ReadInstallation(m.sources.InstallRoot(), id)
	if lockErr == nil && s.Status == "ready" {
		hash, err := connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			return Preparation{}, err
		}
		if hash != s.Fingerprint {
			s.Status = "pending"
			s.Message = "Connector changed; prepare again"
		} else if activeErr != nil || installation.Fingerprint != s.Fingerprint {
			s.Status = "failed"
			s.Message = "CLI installation record is missing; prepare again"
		} else if err = m.verifyInstallation(pkg, installation); err != nil {
			s.Status = "failed"
			s.Message = err.Error()
		}
	}
	if activeErr == nil && installation.Fingerprint != s.Fingerprint {
		s.ActiveVersion, s.ActiveFingerprint = installation.Version, installation.Fingerprint
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
		prefix, err := m.prepareCLI(ctx, pkg, fingerprint)
		if err == nil {
			// init runs in the package directory and may write there; the
			// prepared package is the one after init.
			s.Fingerprint, err = connector.RuntimeFingerprint(pkg.Dir)
		}
		if err == nil && ctx.Err() == nil {
			err = m.activateInstallation(pkg, settings, s.Fingerprint, prefix)
		}
		s.UpdatedAt = time.Now().UTC()
		s.Status = "ready"
		s.Stage = "versionCheck"
		s.ActiveVersion, s.ActiveFingerprint = "", ""
		if err == nil && unpinnedNPMInit(pkg.CLI) {
			s.Message = "init does not pin exact npm package versions; preparing again may install a different CLI"
		}
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
			if previous, err := connector.ReadInstallation(m.sources.InstallRoot(), id); err == nil {
				s.ActiveVersion, s.ActiveFingerprint = previous.Version, previous.Fingerprint
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

// activateInstallation records the verified CLI of a successful preparation
// and keeps only it and the installation it replaces.
func (m *Manager) activateInstallation(pkg connector.Package, settings cliSettings, fingerprint, prefix string) error {
	installation := connector.Installation{ConnectorID: pkg.ID, Version: pkg.Version, Fingerprint: fingerprint, Prefix: prefix, ActivatedAt: time.Now().UTC()}
	if settings.Command != "" {
		entry, err := m.lookupManagedCommand(pkg, settings, installation.Bin())
		if err != nil {
			return err
		}
		installation.Executable = entry
		if installation.ExecutableSHA256, err = connector.CLIFileHash(entry); err != nil {
			return err
		}
	}
	previous, _ := connector.ReadInstallation(m.sources.InstallRoot(), pkg.ID)
	if err := connector.WriteInstallation(m.sources.InstallRoot(), installation); err != nil {
		return err
	}
	connector.PruneInstallations(m.sources.InstallRoot(), pkg.ID, installation.Prefix, previous.Prefix)
	return nil
}

// lookupManagedCommand resolves the managed executable: only bin/ for a package
// that ships one, otherwise the installation's bin before the host PATH.
func (m *Manager) lookupManagedCommand(pkg connector.Package, settings cliSettings, installBin string) (string, error) {
	env, err := m.cliEnvironmentWith(pkg, installBin)
	if err != nil {
		return "", err
	}
	if pkg.BinDir != "" {
		env = hostenv.Set(env, "PATH", pkg.BinDir)
	}
	return hostenv.LookPath(settings.Command, env)
}

// verifyInstallation checks that the pinned executable still resolves to the
// same file with the same content.
func (m *Manager) verifyInstallation(pkg connector.Package, installation connector.Installation) error {
	settings, err := cliSettingsFor(pkg)
	if err != nil {
		return err
	}
	if settings.Command == "" {
		return nil
	}
	entry, err := m.lookupManagedCommand(pkg, settings, installation.Bin())
	if err != nil {
		return fmt.Errorf("CLI executable changed or is missing; prepare again")
	}
	hash, err := connector.CLIFileHash(entry)
	if err != nil || entry != installation.Executable || hash != installation.ExecutableSHA256 {
		return fmt.Errorf("CLI executable changed or is missing; prepare again")
	}
	return nil
}

// requirePrepared accepts the active installation even when a newer package
// version has not been prepared yet, so a failed update keeps the CLI usable.
func (m *Manager) requirePrepared(pkg connector.Package) error {
	installation, err := connector.ReadInstallation(m.sources.InstallRoot(), pkg.ID)
	if err != nil {
		return fmt.Errorf("CLI is not prepared; prepare the connector first")
	}
	return m.verifyInstallation(pkg, installation)
}
