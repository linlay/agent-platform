// Package agentengine owns trusted engine imports and the live engine directory.
// Adapter execution and user profiles belong to the runtime, not this registry.
package agentengine

import (
	"agent-platform/internal/runtimefs"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unicode/utf8"

	dto "agent-platform/internal/contracts/agentengine"
)

type Error string

func (e Error) Error() string { return string(e) }

// Decode applies the existing contract boundary before decoding typed data.
func Decode(name string, data []byte, target any) error {
	if len(data) > 1<<20 || !utf8.Valid(data) || uniqueKeys(json.NewDecoder(bytes.NewReader(data))) != nil || dto.Validate(name, data) != nil {
		return Error("invalid_arguments")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return Error("invalid_arguments")
	}
	return nil
}

type Service struct {
	managementMu      sync.Mutex
	standalone        bool
	node              string
	ownerLock         *os.File
	active            map[string]snapshotUse
	lastMutation      string
	entries           map[string]installation
	mu                sync.Mutex
	root              string
	authority         dto.ManagementBootstrap
	epoch, generation string
	ready, closed     bool
	revision          int64
	changed           func(dto.DirectoryChanged)
	validate          func(string, *Lease) error
}

// SetValidator binds the runtime's protocol check before management starts.
// The callback owns its candidate lease; it must not reenter the registry.
func (s *Service) SetValidator(validate func(string, *Lease) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validate = validate
}

func randomID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// New consumes a private, one-use bootstrap. No bootstrap means no management authority.
func New(root, bootstrapFile string, desktop bool, changed func(dto.DirectoryChanged)) (*Service, error) {
	s := &Service{active: map[string]snapshotUse{}, root: root, epoch: randomID(), generation: randomID(), changed: changed}
	if bootstrapFile == "" {
		return s, nil
	}
	if !desktop || (runtime.GOOS != "darwin" && runtime.GOOS != "windows") {
		return nil, Error("unsupported_deployment")
	}
	if !filepath.IsAbs(bootstrapFile) {
		return nil, Error("artifact_untrusted")
	}
	for _, p := range []string{bootstrapFile, filepath.Dir(bootstrapFile)} {
		if err := runtimefs.PrivateOwned(p); err != nil {
			return nil, Error("artifact_untrusted")
		}
	}
	info, err := os.Stat(bootstrapFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, Error("artifact_untrusted")
	}
	data, err := os.ReadFile(bootstrapFile)
	if err != nil {
		return nil, err
	}
	if err := Decode("ManagementBootstrap", data, &s.authority); err != nil {
		return nil, err
	}
	delivery := s.authority.DeliveryRoot
	if filepath.Base(delivery) != "agent-engines" || filepath.Base(filepath.Dir(delivery)) != "data" {
		return nil, Error("artifact_untrusted")
	}
	if err := runtimefs.PrivateOwned(delivery); err != nil {
		return nil, Error("artifact_untrusted")
	}
	canonical, err := filepath.EvalSymlinks(delivery)
	if err != nil {
		return nil, err
	}
	s.authority.DeliveryRoot = canonical
	if err := os.Remove(bootstrapFile); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Service) Authorize(secret, host, epoch string, mutation bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.authority.ManagementSecret == "" {
		return Error("unsupported_deployment")
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(s.authority.ManagementSecret)) != 1 {
		return Error("management_unauthorized")
	}
	if host != s.authority.HostID {
		return Error("management_forbidden")
	}
	if mutation && epoch != s.epoch {
		return Error("management_epoch_mismatch")
	}
	return nil
}
func (s *Service) Status() dto.ManagementStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return dto.ManagementStatus{APIVersion: 1, HostID: s.authority.HostID, Epoch: s.epoch, Ready: s.ready, AppliedRevision: s.revision}
}
func (s *Service) Close() error {
	s.managementMu.Lock()
	defer s.managementMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		close(entry.retired)
		if entry.pin != nil {
			entry.pin.Close()
		}
	}
	s.entries = nil
	s.closed = true
	s.ready = false
	s.authority.ManagementSecret = ""
	if s.ownerLock != nil {
		_ = os.Remove(filepath.Join(s.root, "agent-engine-installations", "management.json"))
		s.ownerLock.Close()
		s.ownerLock = nil
	}
	return nil
}

// Apply commits one complete desired state after import and runtime validation.
func (s *Service) Apply(operation string, body []byte) (dto.SyncAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ack := dto.SyncAck{APIVersion: 1, DrainingEngineIds: []string{}}
	if s.closed || s.authority.ManagementSecret == "" {
		return ack, Error("unsupported_deployment")
	}
	var revision int64
	var entries []dto.EngineInstallation
	var revoke string
	var canonical []byte
	switch operation {
	case "snapshot":
		var request dto.EngineSnapshot
		if err := Decode("EngineSnapshot", body, &request); err != nil {
			return ack, err
		}
		sort.Slice(request.Entries, func(i, j int) bool { return request.Entries[i].EngineID < request.Entries[j].EngineID })
		revision = request.Revision
		entries = request.Entries
		canonical, _ = json.Marshal(request)
	case "upsert":
		var request dto.EngineUpsert
		if err := Decode("EngineUpsert", body, &request); err != nil {
			return ack, err
		}
		revision = request.Revision
		canonical, _ = json.Marshal(request)
		entries = []dto.EngineInstallation{{EngineID: request.EngineID, PluginID: request.PluginID, PluginVersion: request.PluginVersion, InstallationRef: request.InstallationRef, ArtifactDigest: request.ArtifactDigest, Enabled: request.Enabled}}
	case "revoke":
		var request dto.EngineRevoke
		if err := Decode("EngineRevoke", body, &request); err != nil {
			return ack, err
		}
		revision = request.Revision
		revoke = request.EngineID
		canonical, _ = json.Marshal(request)
	default:
		return ack, Error("invalid_arguments")
	}
	if !s.ready && operation != "snapshot" {
		return ack, Error("registry_not_ready")
	}
	mutation := operation + ":" + string(canonical)
	if revision < s.revision {
		return ack, Error("revision_stale")
	}
	if revision == s.revision {
		if mutation != s.lastMutation {
			return ack, Error("revision_conflict")
		}
		ack.AppliedRevision = s.revision
		ack.Action = "idempotent"
		ack.Ready = s.ready
		ack.DrainingEngineIds = s.drainingIDs()
		return ack, nil
	}
	assembly, err := s.lock(false, "assembly.lock")
	if err != nil {
		return ack, err
	}
	defer func() { assembly.Close(); s.collectUnused() }()
	candidate := map[string]installation{}
	acquired := []*os.File{}
	committed := false
	defer func() {
		if !committed {
			for _, pin := range acquired {
				pin.Close()
			}
		}
	}()
	if operation != "snapshot" {
		for id, entry := range s.entries {
			candidate[id] = entry
		}
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.EngineID] {
			return ack, Error("invalid_arguments")
		}
		seen[entry.EngineID] = true
		installed, err := s.importEntry(entry)
		if err != nil {
			return ack, err
		}
		installed.pin, err = s.pin(installed)
		if err != nil {
			return ack, err
		}
		acquired = append(acquired, installed.pin)
		old, exists := s.entries[entry.EngineID]
		if installed.Enabled && s.validate != nil && (!exists || old.dir != installed.dir || !old.Enabled) {
			pin, err := s.pin(installed)
			if err != nil {
				return ack, err
			}
			var release sync.Once
			lease := installed.lease(pin, func() { release.Do(func() { pin.Close() }) })
			if err := s.validate(entry.EngineID, lease); err != nil {
				return ack, err
			}
		}
		installed.retired = make(chan struct{})
		if old, ok := s.entries[entry.EngineID]; ok && old.dir == installed.dir && old.Enabled == installed.Enabled {
			installed.retired = old.retired
		}
		candidate[entry.EngineID] = installed
	}
	if revoke != "" {
		delete(candidate, revoke)
	}
	if len(candidate) > 256 {
		return ack, Error("invalid_arguments")
	}
	if s.standalone {
		state := dto.EngineSnapshot{APIVersion: 1, Revision: revision, Entries: []dto.EngineInstallation{}}
		for _, entry := range candidate {
			state.Entries = append(state.Entries, entry.EngineInstallation)
		}
		sort.Slice(state.Entries, func(i, j int) bool { return state.Entries[i].EngineID < state.Entries[j].EngineID })
		if err := writePrivateJSON(filepath.Join(s.root, "agent-engine-installations", "installed.json"), state); err != nil {
			return ack, err
		}
	}
	for id, old := range s.entries {
		next, exists := candidate[id]
		if !exists || next.retired != old.retired {
			close(old.retired)
		}
		if !exists || next.pin != old.pin {
			old.pin.Close()
		}
	}
	committed = true
	s.entries = candidate
	ack.DrainingEngineIds = s.drainingIDs()
	s.ready = true
	s.revision = revision
	s.lastMutation = mutation
	ack.AppliedRevision = s.revision
	ack.Action = "applied"
	ack.Ready = true
	s.notifyChanged()
	return ack, nil
}
func (s *Service) notifyChanged() {
	if s.changed != nil && !s.closed {
		s.changed(dto.DirectoryChanged{CatalogGeneration: s.generation, Revision: s.revision})
	}
}
func (s *Service) Directory() dto.EngineDirectory {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := dto.EngineDirectory{APIVersion: 1, CatalogGeneration: s.generation, Revision: s.revision, Ready: s.ready, Engines: []dto.EngineDirectoryEntry{}}
	for _, entry := range s.entries {
		state := "stopped"
		for dir, use := range s.active {
			if use.EngineID != entry.EngineID {
				continue
			}
			if !entry.Enabled || dir != entry.dir {
				state = "draining"
				break
			}
			state = "busy"
		}
		result.Engines = append(result.Engines, dto.EngineDirectoryEntry{EngineID: entry.EngineID, PluginID: entry.PluginID, PluginVersion: entry.PluginVersion, Enabled: entry.Enabled, SyncState: "applied", DependencyState: "ready", AuthState: "unknown", RuntimeState: state})
	}
	sort.Slice(result.Engines, func(i, j int) bool { return result.Engines[i].EngineID < result.Engines[j].EngineID })
	return result
}

// encoding/json otherwise accepts repeated keys with last-value-wins semantics.
func uniqueKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return Error("invalid_arguments")
			}
			seen[name] = true
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
	case json.Delim('['):
		for d.More() {
			if err := uniqueKeys(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}
