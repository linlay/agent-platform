package webapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"agent-platform/internal/config"
)

type Options struct {
	Root     string
	DataRoot string
	Config   config.WebAppsConfig
}
type Service struct {
	mu                      sync.Mutex
	root, dataRoot          string
	config                  config.WebAppsConfig
	maxArchive, maxExpanded int64
	maxFiles                int
	lock                    *os.File
	closed                  bool
	recoveryRequired        bool
	records                 map[string]*Record
	desired                 map[string]Desired
	policies                map[string]Policy
}

func Open(options Options) (*Service, error) {
	if options.Root == "" || options.DataRoot == "" {
		return nil, fmt.Errorf("WebApp roots are required")
	}
	s := &Service{root: options.Root, dataRoot: options.DataRoot, config: options.Config, records: map[string]*Record{}, desired: map[string]Desired{}, policies: map[string]Policy{}}
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
	if err := s.recover(); err != nil {
		return nil, fmt.Errorf("recover WebApp install: %w", err)
	}
	if err := s.collectUploads(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Name() == ".staging" {
			continue
		}
		if !idPattern.MatchString(e.Name()) || !e.IsDir() {
			return nil, fmt.Errorf("unexpected WebApp entry")
		}
		id := e.Name()
		var record Record
		dir := filepath.Join(s.root, id)
		if err := readJSON(filepath.Join(dir, "install.json"), &record); err != nil {
			return nil, err
		}
		if err := validateRecord(record, id); err != nil {
			return nil, err
		}
		digest, err := treeDigest(filepath.Join(dir, "app"), false)
		if err != nil || digest != record.ContentSHA256 {
			return nil, fmt.Errorf("WebApp code integrity mismatch")
		}
		var desired Desired
		if err := readJSON(filepath.Join(dir, "desired.json"), &desired); err != nil {
			return nil, err
		}
		if desired.SchemaVersion != 1 || (desired.State != "stopped" && desired.State != "running") || !opaquePattern.MatchString(desired.Revision) {
			return nil, fmt.Errorf("invalid WebApp desired state")
		}
		var policy Policy
		if err := readJSON(filepath.Join(dir, "policy.json"), &policy); err != nil {
			return nil, err
		}
		if policy.SchemaVersion != 1 || !opaquePattern.MatchString(policy.Revision) {
			return nil, fmt.Errorf("invalid WebApp policy")
		}
		s.records[id] = &record
		s.desired[id] = desired
		s.policies[id] = policy
	}
	keep = true
	return s, nil
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
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Close()
}
func (s *Service) mutationError() error {
	if s.closed {
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
	return Entry{ID: m.ID, Key: m.Key, Label: m.Label, Version: m.Version, Target: m.Target, Revision: record.Revision, DesiredState: state, ObservedState: "stopped", Backend: m.Backend != nil, InstalledAt: record.InstalledAt}
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
func (s *Service) Install(ctx context.Context, req InstallRequest) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return Entry{}, err
	}
	u, archive, err := s.readUpload(req.UploadID)
	if err != nil {
		return Entry{}, err
	}
	if req.SHA256 != u.SHA256 {
		return Entry{}, fail("webapp_upload_changed", 409)
	}
	free, err := availableBytes(s.root)
	if err != nil {
		return Entry{}, err
	}
	if free < uint64(s.maxExpanded)+(32<<20) {
		return Entry{}, fail("webapp_disk_space_insufficient", 507)
	}
	txn := filepath.Join(s.root, ".staging", "install-"+randomID())
	if err := os.Mkdir(txn, 0700); err != nil {
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
	if err := os.Mkdir(code, 0700); err != nil {
		return Entry{}, err
	}
	m, _, _, err := s.extract(ctx, archive, code)
	if err != nil {
		return Entry{}, err
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
	if s.desired[m.ID].State == "running" {
		return Entry{}, fail("webapp_update_requires_stop", 409)
	}
	if old == nil {
		if _, err := os.Lstat(filepath.Join(s.root, m.ID)); !os.IsNotExist(err) {
			return Entry{}, fail("webapp_installation_conflict", 409)
		}
	}
	digest, err := treeDigest(code, true)
	if err != nil {
		return Entry{}, err
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
	if err := writeJSON(filepath.Join(txn, "journal.json"), j); err != nil {
		return Entry{}, err
	}
	journalWritten = true
	rollback := func(cause error) (Entry, error) {
		if err := s.rollback(txn, j); err != nil {
			s.recoveryRequired = true
			return Entry{}, fmt.Errorf("WebApp rollback failed: %w", err)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mutationError(); err != nil {
		return err
	}
	r := s.records[id]
	if r == nil {
		return fail("webapp_not_found", 404)
	}
	if r.Revision != revision {
		return fail("webapp_revision_conflict", 409)
	}
	if s.desired[id].State == "running" {
		return fail("webapp_uninstall_requires_stop", 409)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A durable deletion journal makes a crash between detaching code and
	// optional data deletion retryable, without reviving an uninstalled app.
	txn := filepath.Join(s.root, ".staging", "delete-"+randomID())
	if err := os.Mkdir(txn, 0700); err != nil {
		return err
	}
	j := deleteJournal{SchemaVersion: 1, AppID: id, PurgeData: purgeData}
	if err := writeJSON(filepath.Join(txn, "delete.json"), j); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(txn)); err != nil {
		s.recoveryRequired = true
		return err
	}
	if err := s.finishDelete(txn, j); err != nil {
		s.recoveryRequired = true
		return err
	}
	delete(s.records, id)
	delete(s.desired, id)
	delete(s.policies, id)
	return nil
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
