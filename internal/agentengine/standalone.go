package agentengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/runtimefs"
)

// NewStandalone owns the deployment's installed packages. The local management
// secret is per-process; the host identity and committed snapshot survive restart.
func NewStandalone(root, node string, changed func(dto.DirectoryChanged)) (*Service, error) {
	if !filepath.IsAbs(root) || (node != "" && !filepath.IsAbs(node)) {
		return nil, Error("invalid_arguments")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		return nil, Error("unsupported_deployment")
	}
	s, err := New(root, "", false, changed)
	if err != nil {
		return nil, err
	}
	lock, err := s.lock(false, "standalone.lock")
	if err != nil {
		return nil, err
	}
	s.ownerLock = lock
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	dir := filepath.Join(root, "agent-engine-installations")
	if err = realDirectories(root, dir); err != nil {
		return nil, err
	}
	identity := filepath.Join(dir, "host.json")
	var host string
	data, err := os.ReadFile(identity)
	if os.IsNotExist(err) {
		host = randomID()
		err = writePrivateJSON(identity, host)
	} else if err == nil {
		err = runtimefs.PrivateOwned(identity)
		if err == nil {
			err = json.Unmarshal(data, &host)
		}
	}
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, Error("artifact_untrusted")
	}
	delivery := filepath.Join(dir, "data", "agent-engines")
	if err = realDirectories(root, delivery); err != nil {
		return nil, err
	}
	s.authority = dto.ManagementBootstrap{SchemaVersion: 1, HostID: host, LaunchNonce: randomID(), ManagementSecret: randomID(), DeliveryRoot: delivery}
	s.standalone = true
	s.node = node
	success = true
	return s, nil
}

// Restore runs after the runtime validator is attached and before requests arrive.
func (s *Service) Restore() error {
	if !s.standalone {
		return Error("unsupported_deployment")
	}
	state := dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{}}
	file := filepath.Join(s.root, "agent-engine-installations", "installed.json")
	data, err := os.ReadFile(file)
	if err == nil {
		if err = runtimefs.PrivateOwned(file); err != nil {
			return err
		}
		if err = Decode("EngineSnapshot", data, &state); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err = json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.Apply("snapshot", data)
	return err
}

func writePrivateJSON(target string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), target)
}

// ChangePlugin first closes admission. Active leases retain their program and
// complete normally; disable/uninstall returns engine_busy until they drain.
func (s *Service) ChangePlugin(pluginID, action string) (dto.SyncAck, error) {
	s.managementMu.Lock()
	defer s.managementMu.Unlock()
	if !s.standalone {
		return dto.SyncAck{}, Error("unsupported_deployment")
	}
	if action != "enable" && action != "disable" && action != "uninstall" {
		return dto.SyncAck{}, Error("invalid_arguments")
	}
	entries := s.installedEntries()
	found := false
	for i := range entries {
		if entries[i].PluginID == pluginID {
			found = true
			entries[i].Enabled = action == "enable"
		}
	}
	if !found {
		return dto.SyncAck{}, Error("engine_unavailable")
	}
	ack, err := s.applyInstalled(entries)
	if err != nil {
		return ack, err
	}
	if action != "enable" {
		for _, id := range ack.DrainingEngineIds {
			if strings.HasPrefix(id, pluginID+"/") {
				return ack, Error("engine_busy")
			}
		}
	}
	if action != "uninstall" {
		return ack, nil
	}
	next := []dto.EngineInstallation{}
	for _, entry := range entries {
		if entry.PluginID != pluginID {
			next = append(next, entry)
		}
	}
	ack, err = s.applyInstalled(next)
	if err != nil {
		return ack, err
	}
	// Program snapshots use separate leases. Credentials and Chat bindings survive
	// removal, so reinstalling the engine can explicitly resume its own sessions.
	files, err := os.ReadDir(s.authority.DeliveryRoot)
	if err != nil {
		return ack, err
	}
	for _, file := range files {
		if !file.IsDir() || strings.HasPrefix(file.Name(), ".") {
			continue
		}
		dir := filepath.Join(s.authority.DeliveryRoot, file.Name())
		var record dto.TrustedDeliveryRecord
		if readContract(dir, "record.json", "TrustedDeliveryRecord", &record) != nil || record.OwnerHostID != s.authority.HostID || record.InstallationRef != file.Name() || record.PluginID != pluginID {
			continue
		}
		if err = os.RemoveAll(dir); err != nil {
			return ack, err
		}
	}
	return ack, nil
}
