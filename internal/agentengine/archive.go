package agentengine

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	dto "agent-platform/internal/contracts/agentengine"
)

const MaxArchiveBytes int64 = 512 << 20

// InstallArchive accepts the same Manifest ZIP as Desktop. It never invokes a
// package manager or Shell; the runtime validator checks the staged ACP program.
func (s *Service) InstallArchive(source io.ReaderAt, size int64, overwrite bool) (dto.SyncAck, error) {
	s.managementMu.Lock()
	defer s.managementMu.Unlock()
	if !s.standalone {
		return dto.SyncAck{}, Error("unsupported_deployment")
	}
	if source == nil || size <= 0 || size > MaxArchiveBytes {
		return dto.SyncAck{}, Error("invalid_arguments")
	}
	archive, err := zip.NewReader(source, size)
	if err != nil {
		return dto.SyncAck{}, Error("artifact_untrusted")
	}
	stage, err := os.MkdirTemp(s.authority.DeliveryRoot, ".install-")
	if err != nil {
		return dto.SyncAck{}, err
	}
	defer os.RemoveAll(stage)
	unpack := filepath.Join(stage, "unpack")
	if err = os.Mkdir(unpack, 0700); err != nil {
		return dto.SyncAck{}, err
	}
	if err = extractEngineArchive(archive, unpack); err != nil {
		return dto.SyncAck{}, err
	}
	top, err := os.ReadDir(unpack)
	if err != nil || len(top) != 1 || !top[0].IsDir() {
		return dto.SyncAck{}, Error("artifact_untrusted")
	}
	pkg := filepath.Join(unpack, top[0].Name())
	var manifest dto.EnginePluginManifest
	if err = readContract(pkg, "manifest.json", "EnginePluginManifest", &manifest); err != nil {
		return dto.SyncAck{}, err
	}
	if manifest.ID != top[0].Name() || manifest.Desktop.BundleTopLevelDir != manifest.ID {
		return dto.SyncAck{}, Error("manifest_unsupported")
	}
	if manifest.Platform.Os != runtime.GOOS || manifest.Platform.Arch != runtime.GOARCH {
		return dto.SyncAck{}, Error("unsupported_deployment")
	}
	entries := s.installedEntries()
	next := []dto.EngineInstallation{}
	enabled := true
	for _, entry := range entries {
		if entry.PluginID == manifest.ID {
			if !overwrite {
				return dto.SyncAck{}, Error("plugin_exists")
			}
			enabled = entry.Enabled
		} else {
			next = append(next, entry)
		}
	}
	ref := randomID()
	digest, err := fingerprint(pkg)
	if err != nil {
		return dto.SyncAck{}, err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.NewSectionReader(source, 0, size)); err != nil {
		return dto.SyncAck{}, err
	}
	record := dto.TrustedDeliveryRecord{SchemaVersion: 1, InstallationRef: ref, OwnerHostID: s.authority.HostID, PluginID: manifest.ID, PluginVersion: manifest.Version, Platform: manifest.Platform, ArtifactDigest: digest, Source: dto.DeliverySource{Kind: "local-import", AuthorizationRef: "local-import:" + ref, ArchiveDigest: fmt.Sprintf("sha256:%x", hash.Sum(nil))}}
	if err = os.Rename(pkg, filepath.Join(stage, "package")); err != nil {
		return dto.SyncAck{}, err
	}
	if err = os.Remove(unpack); err != nil {
		return dto.SyncAck{}, err
	}
	if err = writePrivateJSON(filepath.Join(stage, "record.json"), record); err != nil {
		return dto.SyncAck{}, err
	}
	destination := filepath.Join(s.authority.DeliveryRoot, ref)
	if err = os.Rename(stage, destination); err != nil {
		return dto.SyncAck{}, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(destination)
		}
	}()
	for _, engine := range manifest.Resources.AgentEngines {
		next = append(next, dto.EngineInstallation{EngineID: manifest.ID + "/" + engine.ID, PluginID: manifest.ID, PluginVersion: manifest.Version, InstallationRef: ref, ArtifactDigest: digest, Enabled: enabled})
	}
	ack, err := s.applyInstalled(next)
	if err != nil {
		return ack, err
	}
	committed = true
	return ack, nil
}

func (s *Service) installedEntries() []dto.EngineInstallation {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := []dto.EngineInstallation{}
	for _, entry := range s.entries {
		entries = append(entries, entry.EngineInstallation)
	}
	return entries
}
func (s *Service) applyInstalled(entries []dto.EngineInstallation) (dto.SyncAck, error) {
	state := dto.EngineSnapshot{APIVersion: 1, Revision: s.Status().AppliedRevision + 1, Entries: entries}
	data, err := json.Marshal(state)
	if err != nil {
		return dto.SyncAck{}, err
	}
	return s.Apply("snapshot", data)
}

func extractEngineArchive(archive *zip.Reader, root string) error {
	if len(archive.File) > 100000 {
		return Error("artifact_untrusted")
	}
	seen := map[string]bool{}
	var total int64
	for _, entry := range archive.File {
		name := strings.TrimSuffix(entry.Name, "/")
		mode := entry.Mode()
		if name == "" || !filepath.IsLocal(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || seen[name] || (!mode.IsRegular() && !mode.IsDir()) || mode&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid) != 0 {
			return Error("artifact_untrusted")
		}
		seen[name] = true
		if entry.UncompressedSize64 > 512<<20 {
			return Error("artifact_untrusted")
		}
		total += int64(entry.UncompressedSize64)
		if total > 2<<30 {
			return Error("artifact_untrusted")
		}
		destination := filepath.Join(root, filepath.FromSlash(name))
		if mode.IsDir() {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		permissions := os.FileMode(0644)
		if mode.Perm()&0111 != 0 {
			permissions = 0755
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
		if err != nil {
			input.Close()
			return err
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(entry.UncompressedSize64) {
			return Error("artifact_untrusted")
		}
	}
	return nil
}
