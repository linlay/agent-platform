package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"agent-platform/internal/config"
)

// SDKStateDir holds bridge ownership receipts beside the installations. It is
// created lazily by webappbridge and is never an application directory.
const SDKStateDir = ".sdk"

type Options struct {
	Root     string
	DataRoot string
	Config   config.WebAppsConfig
	Runner   Runner
}
type Service struct {
	mu sync.Mutex
	// packageMu serializes archive hashing and extraction. It is never taken
	// while holding mu, so page and SDK traffic does not wait for package I/O.
	packageMu               sync.Mutex
	closeMu                 sync.Mutex
	root, dataRoot          string
	config                  config.WebAppsConfig
	maxArchive, maxExpanded int64
	maxFiles                int
	lock                    *os.File
	closed                  bool
	recoveryRequired        bool
	// unavailable keeps installations that failed startup checks untouched
	// for manual recovery; the rest of the host and Platform keep running.
	unavailable      map[string]string
	records          map[string]*Record
	desired          map[string]Desired
	policies         map[string]Policy
	active           map[string]*activeApp
	controls         map[string]*sync.Mutex
	closing          bool
	installing       map[string]bool
	operations       map[string]*Operation
	operationContext context.Context
	operationCancel  context.CancelFunc
	operationWG      sync.WaitGroup
	runner           Runner
}

func Open(options Options) (*Service, error) {
	if options.Root == "" || options.DataRoot == "" {
		return nil, fmt.Errorf("WebApp roots are required")
	}
	s := &Service{root: options.Root, dataRoot: options.DataRoot, config: options.Config, runner: options.Runner, records: map[string]*Record{}, desired: map[string]Desired{}, policies: map[string]Policy{}, active: map[string]*activeApp{}, controls: map[string]*sync.Mutex{}, installing: map[string]bool{}, unavailable: map[string]string{}}
	s.operations = map[string]*Operation{}
	s.operationContext, s.operationCancel = context.WithCancel(context.Background())
	s.maxArchive = int64(options.Config.MaxArchiveMiB) << 20
	s.maxExpanded = int64(options.Config.MaxExtractedMiB) << 20
	s.maxFiles = options.Config.MaxFiles
	if s.maxArchive <= 0 || s.maxExpanded < s.maxArchive || s.maxFiles < 1 {
		return nil, fmt.Errorf("invalid WebApp limits")
	}
	for _, dir := range []string{s.root, s.dataRoot, filepath.Join(s.root, ".staging"), s.uploads(), filepath.Join(filepath.Dir(s.root), ".lock", "webapps")} {
		if err := ensureDirectory(dir); err != nil {
			return nil, err
		}
	}
	var err error
	s.root, err = filepath.EvalSymlinks(s.root)
	if err != nil {
		return nil, err
	}
	s.dataRoot, err = filepath.EvalSymlinks(s.dataRoot)
	if err != nil {
		return nil, err
	}
	s.lock, err = os.OpenFile(filepath.Join(filepath.Dir(s.root), ".lock", "webapps", "instance.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := acquireInstance(s.lock); err != nil {
		s.lock.Close()
		return nil, fail("webapp_host_already_running", 409)
	}
	keep := false
	defer func() {
		if !keep {
			s.lock.Close()
		}
	}()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	// No directory switch or recovery is allowed while a previous helper
	// still owns an application tree, including after a Platform crash. Such
	// an installation stays untouched; other applications continue.
	if checker, ok := s.runner.(interface{ ConfirmStopped(string) bool }); ok {
		for _, e := range entries {
			id := e.Name()
			if !idPattern.MatchString(id) || !e.IsDir() {
				continue
			}
			if !checker.ConfirmStopped(id) {
				s.markUnavailable(id, "webapp_previous_generation_alive", nil)
				continue
			}
			if err := removeTree(filepath.Join(s.root, id, ".generations")); err != nil {
				s.markUnavailable(id, "webapp_installation_invalid", err)
			}
		}
	}
	s.recover()
	if err := s.collectUploads(); err != nil {
		log.Printf("WebApp uploads not collected: %v", err)
	}
	for _, e := range entries {
		id := e.Name()
		if id == ".staging" || id == SDKStateDir {
			continue
		}
		if !idPattern.MatchString(id) {
			log.Printf("WebApp root has unexpected entry %q; ignoring it", id)
			continue
		}
		if s.unavailable[id] != "" {
			continue
		}
		if !e.IsDir() {
			s.markUnavailable(id, "webapp_installation_invalid", fmt.Errorf("not a directory"))
			continue
		}
		if err := s.load(id); err != nil {
			s.markUnavailable(id, "webapp_installation_invalid", err)
		}
	}
	keep = true
	return s, nil
}

// markUnavailable must run before the service is shared or with mu held.
func (s *Service) markUnavailable(id, code string, cause error) {
	if s.unavailable[id] == "" {
		s.unavailable[id] = code
	}
	log.Printf("WebApp %s left for manual recovery (%s): %v", id, code, cause)
}

func (s *Service) load(id string) error {
	// The directory listing was taken before recovery, which may have removed
	// an uncommitted first installation.
	dir := filepath.Join(s.root, id)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	var record Record
	if err := readJSON(filepath.Join(dir, "install.json"), &record); err != nil {
		return err
	}
	if err := validateRecord(record, id); err != nil {
		return err
	}
	if err := verifyInstalledCode(filepath.Join(dir, "app"), record); err != nil {
		return err
	}
	var desired Desired
	if err := readJSON(filepath.Join(dir, "desired.json"), &desired); err != nil {
		return err
	}
	if desired.SchemaVersion != 1 || (desired.State != "stopped" && desired.State != "running") || !opaquePattern.MatchString(desired.Revision) {
		return fmt.Errorf("invalid WebApp desired state")
	}
	var policy Policy
	if err := readJSON(filepath.Join(dir, "policy.json"), &policy); err != nil {
		return err
	}
	if policy.SchemaVersion != 1 || !opaquePattern.MatchString(policy.Revision) || validatePolicy(policy) != nil {
		return fmt.Errorf("invalid WebApp policy")
	}
	s.records[id] = &record
	s.desired[id] = desired
	s.policies[id] = policy
	return nil
}

// Issues reports bounded codes for installations kept for manual recovery
// and for a host-wide mutation stop. Logs carry the application details.
func (s *Service) Issues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	if s.recoveryRequired {
		seen["webapp_recovery_required"] = true
	}
	for _, code := range s.unavailable {
		seen[code] = true
	}
	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}
func validateRecord(r Record, id string) error {
	if r.SchemaVersion != 1 || r.Manifest.ID != id || !idPattern.MatchString(id) || !opaquePattern.MatchString(r.Revision) || !opaquePattern.MatchString(r.InstallationID) || !isSHA256(r.ArchiveSHA256) || !isSHA256(r.ContentSHA256) {
		return fmt.Errorf("invalid WebApp installation record")
	}
	b, err := json.Marshal(r.Manifest)
	if err != nil {
		return err
	}
	if _, err := ParseManifest(b); err != nil {
		return err
	}
	return validateCompose(r.Manifest)
}
func (s *Service) mutationError() error {
	if s.closed || s.closing {
		return fail("webapp_host_stopped", 503)
	}
	if s.recoveryRequired {
		return fail("webapp_recovery_required", 503)
	}
	return nil
}
func (s *Service) entry(record *Record) Entry {
	m := record.Manifest
	state := s.desired[m.ID].State
	observed := "stopped"
	generation := ""
	if a := s.active[m.ID]; a != nil {
		observed = a.state
		generation = a.generation
	}
	if s.installing[m.ID] {
		observed = "updating"
	}
	lastError := ""
	restartCount := 0
	var exitCode *int
	if a := s.active[m.ID]; a != nil {
		lastError = a.lastError
		restartCount = a.restartCount
		exitCode = a.exitCode
	}
	return Entry{LastError: lastError, RestartCount: restartCount, ExitCode: exitCode, ID: m.ID, Key: m.Key, Label: m.Label, Version: m.Version, Target: m.Target, Revision: record.Revision, StateRevision: s.desired[m.ID].Revision, DesiredState: state, ObservedState: observed, Generation: generation, Backend: m.Backend != nil, InstalledAt: record.InstalledAt}
}
func (s *Service) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Entry{}
	for _, r := range s.records {
		items = append(items, s.entry(r))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
func (s *Service) Get(id string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return Entry{}, err
	}
	if r := s.records[id]; r != nil {
		return s.entry(r), nil
	}
	return Entry{}, fail("webapp_not_found", 404)
}

// prepareInstall hashes and extracts the upload without holding mu. On
// success the caller owns txn, which holds the frozen code in txn/new.
func (s *Service) prepareInstall(ctx context.Context, req InstallRequest) (Upload, string, Manifest, string, error) {
	s.packageMu.Lock()
	defer s.packageMu.Unlock()
	u, archive, err := s.readUpload(req.UploadID)
	if err != nil {
		return u, "", Manifest{}, "", err
	}
	if req.SHA256 != u.SHA256 {
		return u, "", Manifest{}, "", fail("webapp_upload_changed", 409)
	}
	free, err := availableBytes(s.root)
	if err != nil {
		return u, "", Manifest{}, "", err
	}
	if free < uint64(s.maxExpanded)+(32<<20) {
		return u, "", Manifest{}, "", fail("webapp_disk_space_insufficient", 507)
	}
	txn := filepath.Join(s.root, ".staging", "install-"+randomID())
	if err := os.Mkdir(txn, 0700); err != nil {
		return u, "", Manifest{}, "", err
	}
	code := filepath.Join(txn, "new")
	m, digest, err := func() (Manifest, string, error) {
		if err := os.Mkdir(code, 0700); err != nil {
			return Manifest{}, "", err
		}
		m, _, _, err := s.extract(ctx, archive, code)
		if err != nil {
			return Manifest{}, "", err
		}
		digest, err := treeDigest(code, true)
		return m, digest, err
	}()
	if err != nil {
		_ = removeTree(txn)
		return u, "", Manifest{}, "", err
	}
	return u, txn, m, digest, nil
}
func (s *Service) install(ctx context.Context, req InstallRequest, resume bool, validate func(Entry) error) (Entry, error) {
	s.mu.Lock()
	err := s.mutationError()
	s.mu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	u, txn, m, digest, err := s.prepareInstall(ctx, req)
	if err != nil {
		return Entry{}, err
	}
	journalWritten := false
	keep := false
	defer func() {
		if !keep && !journalWritten {
			_ = removeTree(txn)
		}
	}()
	code := filepath.Join(txn, "new")
	s.mu.Lock()
	defer s.mu.Unlock()
	// Close or a failed recovery may have happened during extraction.
	if err := s.mutationError(); err != nil {
		return Entry{}, err
	}
	if s.unavailable[m.ID] != "" {
		return Entry{}, fail("webapp_recovery_required", 503)
	}
	old := s.records[m.ID]
	revision := ""
	if old != nil {
		revision = old.Revision
	}
	if revision != req.ExpectedRevision {
		return Entry{}, fail("webapp_revision_conflict", 409)
	}
	if old != nil && old.Manifest.Key != m.Key {
		return Entry{}, fail("webapp_package_identity_conflict", 409)
	}
	if (!resume && s.desired[m.ID].State == "running") || s.active[m.ID] != nil {
		return Entry{}, fail("webapp_update_requires_stop", 409)
	}
	if old == nil {
		if _, err := os.Lstat(filepath.Join(s.root, m.ID)); !os.IsNotExist(err) {
			return Entry{}, fail("webapp_installation_conflict", 409)
		}
	}
	if old != nil && old.Manifest.Version == m.Version && old.ContentSHA256 != digest {
		return Entry{}, fail("webapp_version_content_conflict", 409)
	}
	installationID := randomID()
	if old != nil {
		installationID = old.InstallationID
	}
	record := Record{SchemaVersion: 1, Manifest: m, Revision: randomID(), InstallationID: installationID, ArchiveSHA256: u.SHA256, ContentSHA256: digest, InstalledAt: nowMillis()}
	j := installJournal{SchemaVersion: 1, AppID: m.ID, Old: old, Next: record, Phase: "prepared"}
	if resume {
		d := s.desired[m.ID]
		j.OldDesired = &d
	}
	if err := writeJSON(filepath.Join(txn, "journal.json"), j); err != nil {
		return Entry{}, err
	}
	journalWritten = true
	rollback := func(cause error) (Entry, error) {
		if err := s.rollback(txn, j); err != nil {
			s.recoveryRequired = true
			return Entry{}, fmt.Errorf("WebApp rollback failed: %w", err)
		}
		if old != nil {
			s.records[m.ID] = old
		}
		if j.OldDesired != nil {
			s.desired[m.ID] = *j.OldDesired
		}
		if old == nil {
			delete(s.desired, m.ID)
			delete(s.policies, m.ID)
		}
		journalWritten = false
		return Entry{}, cause
	}
	if err := syncDir(filepath.Dir(txn)); err != nil {
		return rollback(err)
	}
	if ctx.Err() != nil {
		return rollback(ctx.Err())
	}
	dir := filepath.Join(s.root, m.ID)
	if err := ensureDirectory(dir); err != nil {
		return rollback(err)
	}
	if old != nil {
		if err := renamePackage(filepath.Join(dir, "app"), filepath.Join(txn, "old")); err != nil {
			return rollback(err)
		}
		if err := syncDir(dir); err != nil {
			return rollback(err)
		}
		if err := syncDir(txn); err != nil {
			return rollback(err)
		}
	}
	if err := renamePackage(code, filepath.Join(dir, "app")); err != nil {
		return rollback(err)
	}
	if err := syncDir(dir); err != nil {
		return rollback(err)
	}
	if err := syncDir(txn); err != nil {
		return rollback(err)
	}
	if err := writeJSON(filepath.Join(dir, "install.json"), record); err != nil {
		return rollback(err)
	}
	if old == nil {
		d := Desired{SchemaVersion: 1, State: "stopped", Revision: randomID()}
		p := Policy{SchemaVersion: 1, Revision: randomID(), Skills: []string{}, Connectors: []ConnectorScope{}, ConnectPorts: []int{}}
		for name, value := range map[string]any{"desired.json": d, "policy.json": p, "user-config.json": defaultUserConfig(m)} {
			if err := writeJSON(filepath.Join(dir, name), value); err != nil {
				return rollback(err)
			}
		}
		s.desired[m.ID] = d
		s.policies[m.ID] = p
	}
	if validate != nil {
		s.records[m.ID] = &record
		e := s.entry(&record)
		s.mu.Unlock()
		validationError := validate(e)
		s.mu.Lock()
		if validationError != nil {
			code, _ := ErrorCode(validationError)
			if code == "webapp_stop_incomplete" {
				s.recoveryRequired = true
				keep = true
				return Entry{}, validationError
			}
			return rollback(validationError)
		}
	}
	j.Phase = "committed"
	if err := syncDir(s.root); err != nil {
		return rollback(err)
	}
	if err := writeJSON(filepath.Join(txn, "journal.json"), j); err != nil {
		if old == nil {
			delete(s.desired, m.ID)
			delete(s.policies, m.ID)
		}
		return rollback(err)
	}
	s.records[m.ID] = &record
	keep = true
	// Cleanup is repeatable. A committed journal may be removed on next startup.
	if err := removeTree(txn); err != nil {
		s.recoveryRequired = true
		return Entry{}, fail("webapp_install_committed_cleanup_pending", 503)
	}
	_ = removeTree(filepath.Join(s.uploads(), req.UploadID))
	return s.entry(&record), nil
}
func (s *Service) Uninstall(ctx context.Context, id, revision string, purgeData bool) error {
	if !idPattern.MatchString(id) {
		return fail("webapp_not_found", 404)
	}
	control := s.control(id)
	control.Lock()
	defer control.Unlock()
	txn, j, err := s.beginUninstall(ctx, id, revision, purgeData)
	if err != nil {
		return err
	}
	// The durable journal already removed the app from service; the control
	// lock keeps a reinstall of the same ID out while the trees are deleted.
	if err := s.finishDelete(txn, j); err != nil {
		s.mu.Lock()
		s.recoveryRequired = true
		s.mu.Unlock()
		return err
	}
	return nil
}
func (s *Service) beginUninstall(ctx context.Context, id, revision string, purgeData bool) (string, deleteJournal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := deleteJournal{SchemaVersion: 1, AppID: id, PurgeData: purgeData}
	if err := s.mutationError(); err != nil {
		return "", j, err
	}
	r := s.records[id]
	if r == nil {
		return "", j, fail("webapp_not_found", 404)
	}
	if r.Revision != revision {
		return "", j, fail("webapp_revision_conflict", 409)
	}
	if s.desired[id].State == "running" || s.active[id] != nil {
		return "", j, fail("webapp_uninstall_requires_stop", 409)
	}
	if ctx.Err() != nil {
		return "", j, ctx.Err()
	}
	// A durable deletion journal makes a crash between detaching code and
	// optional data deletion retryable, without reviving an uninstalled app.
	txn := filepath.Join(s.root, ".staging", "delete-"+randomID())
	if err := os.Mkdir(txn, 0700); err != nil {
		return "", j, err
	}
	if err := writeJSON(filepath.Join(txn, "delete.json"), j); err != nil {
		return "", j, err
	}
	if err := syncDir(filepath.Dir(txn)); err != nil {
		s.recoveryRequired = true
		return "", j, err
	}
	delete(s.records, id)
	delete(s.desired, id)
	delete(s.policies, id)
	return txn, j, nil
}
func defaultUserConfig(m Manifest) map[string]any {
	values := map[string]any{}
	if m.UserConfig != nil {
		for _, f := range m.UserConfig.Fields {
			if f.Default != nil {
				values[f.Name] = f.Default
			} else if f.Type == "boolean" {
				values[f.Name] = false
			}
		}
	}
	return values
}

func (s *Service) BackendAvailable() bool { return s.runner != nil && s.runner.Ready() }
