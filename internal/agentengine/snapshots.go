package agentengine

import (
	dto "agent-platform/internal/contracts/agentengine"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"agent-platform/internal/runtimefs"
)

func (s *Service) lock(shared bool, parts ...string) (*os.File, error) {
	f, err := runtimefs.OpenLock(s.root, append([]string{"agent-engines"}, parts...)...)
	if err != nil {
		return nil, err
	}
	var ok bool
	if shared {
		ok, err = runtimefs.TryShared(f)
	} else {
		ok, err = runtimefs.TryExclusive(f)
	}
	if err != nil || !ok {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, Error("engine_busy")
	}
	return f, nil
}
func (s *Service) pin(entry installation) (*os.File, error) {
	return s.lock(true, "leases", entry.PluginID, entry.resource.ID, strings.TrimPrefix(entry.ArtifactDigest, "sha256:")+".lock")
}

type Lease struct {
	// Pin is inherited only by the process guardian; callers must not close it.
	Pin *os.File
	// Retirement closes admission; the holder drains work before releasing files.
	Retired                       <-chan struct{}
	Entry, Command                string
	PluginVersion, ArtifactDigest string
	Args                          []string
	PrefixArgs                    []string
	BinDirs                       []string
	TerminalMethods               []dto.TerminalAuthLaunch
	AgentMethods                  []string
	release                       func()
}

func (l *Lease) Release() { l.release() }
func (s *Service) Acquire(engineID string) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.closed {
		return nil, Error("registry_not_ready")
	}
	entry, ok := s.entries[engineID]
	if !ok || !entry.Enabled {
		return nil, Error("engine_unavailable")
	}
	pin, err := s.pin(entry)
	if err != nil {
		return nil, err
	}
	if actual, err := fingerprint(entry.dir); err != nil || actual != entry.ArtifactDigest {
		pin.Close()
		return nil, Error("artifact_digest_mismatch")
	}
	if err := validateDependencies(entry.dir, entry.node, entry.resource); err != nil {
		pin.Close()
		return nil, err
	}
	use := s.active[entry.dir]
	use.EngineID = entry.EngineID
	use.Count++
	s.active[entry.dir] = use
	s.notifyChanged()
	var once sync.Once
	return entry.lease(pin, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			pin.Close()
			use := s.active[entry.dir]
			use.Count--
			if use.Count == 0 {
				delete(s.active, entry.dir)
			} else {
				s.active[entry.dir] = use
			}
			s.collectUnused()
			s.notifyChanged()
		})
	}), nil
}

// Collect only removes verified, unleased engine versions. Unknown or interrupted
// staging files are left alone; current/import/process references share OS locks.
func (s *Service) Collect() error {
	assembly, err := s.lock(false, "assembly.lock")
	if err != nil {
		return err
	}
	defer assembly.Close()
	root := filepath.Join(s.root, "ru-agent-engines")
	plugins, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, plugin := range plugins {
		if !plugin.IsDir() || strings.HasPrefix(plugin.Name(), ".") {
			continue
		}
		ids, err := os.ReadDir(filepath.Join(root, plugin.Name()))
		if err != nil {
			return err
		}
		for _, id := range ids {
			if !id.IsDir() || strings.HasPrefix(id.Name(), ".") {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(root, plugin.Name(), id.Name()))
			if err != nil {
				return err
			}
			for _, version := range versions {
				digest := version.Name()
				b, e := hex.DecodeString(digest)
				if e != nil || len(b) != 32 || digest != strings.ToLower(digest) || !version.IsDir() {
					continue
				}
				// RemoveUnused acquires this lock once; Windows byte-range locks are not reentrant.
				lock, err := runtimefs.OpenLock(s.root, "agent-engines", "leases", plugin.Name(), id.Name(), digest+".lock")
				if err != nil {
					return err
				}
				dir := filepath.Join(root, plugin.Name(), id.Name(), digest)
				e = runtimefs.RemoveUnused(dir, lock)
				lock.Close()
				if e != nil {
					return e
				}
			}
		}
	}
	return nil
}

type snapshotUse struct {
	EngineID string
	Count    int
}

func (s *Service) drainingIDs() []string {
	ids := map[string]bool{}
	for dir, use := range s.active {
		current, ok := s.entries[use.EngineID]
		if !ok || !current.Enabled || current.dir != dir {
			ids[use.EngineID] = true
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
func (s *Service) collectUnused() {
	if err := s.Collect(); err != nil && err != Error("engine_busy") {
		log.Printf("[agent-engine] snapshot collection deferred: %v", err)
	}
}

func (entry installation) lease(pin *os.File, release func()) *Lease {
	var binDirs []string
	if entry.node != "" && entry.resource.Launch.Runtime != "node" {
		binDirs = append(binDirs, filepath.Dir(entry.node))
	}
	for _, dependency := range entry.resource.Dependencies {
		if dependency.Entry != nil {
			binDirs = append(binDirs, filepath.Dir(filepath.Join(entry.dir, filepath.FromSlash(*dependency.Entry))))
		}
	}
	var methods []dto.TerminalAuthLaunch
	var agentMethods []string
	if entry.resource.Authentication != nil {
		agentMethods = append([]string(nil), entry.resource.Authentication.AgentMethods...)
		for _, m := range entry.resource.Authentication.TerminalMethods {
			methods = append(methods, dto.TerminalAuthLaunch{MethodID: m.MethodID, Args: append([]string(nil), m.Args...)})
		}
	}
	command := filepath.Join(entry.dir, filepath.FromSlash(entry.resource.Launch.Entry))
	var prefix []string
	if entry.resource.Launch.Runtime == "node" {
		prefix = []string{command}
		command = entry.node
	}
	return &Lease{Pin: pin, Retired: entry.retired, TerminalMethods: methods, AgentMethods: agentMethods, PluginVersion: entry.PluginVersion, ArtifactDigest: entry.ArtifactDigest, BinDirs: binDirs, Entry: filepath.Join(entry.dir, filepath.FromSlash(entry.resource.Launch.Entry)), Command: command, PrefixArgs: prefix, Args: append([]string{}, entry.resource.Launch.Args...), release: release}
}
