package agentengine_test

import (
	"agent-platform/internal/connector"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	engines "agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
)

func bootstrap(t *testing.T) (string, string, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS managed-host fixture; native Windows ACL and delivery validation remains T10")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(root, "desktop")
	delivery := filepath.Join(desktop, "data", "agent-engines")
	if err := os.MkdirAll(delivery, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "bootstrap.json")
	data, _ := json.Marshal(dto.ManagementBootstrap{SchemaVersion: 1, HostID: "host-a", LaunchNonce: "launch-a", ManagementSecret: strings.Repeat("a", 64), DeliveryRoot: delivery})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return root, file, delivery
}
func TestBootstrapIsConsumedAndAuthorityStartsUnready(t *testing.T) {
	root, file, _ := bootstrap(t)
	s, err := engines.New(filepath.Join(root, "runtime"), file, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("bootstrap must be consumed")
	}
	status := s.Status()
	if status.Ready || status.Epoch == "" || status.HostID != "host-a" {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, secret := range []string{"", "Bearer ordinary-app-token", strings.Repeat("b", 64)} {
		if err := s.Authorize(secret, "host-a", status.Epoch, true); err == nil {
			t.Fatal("ordinary authority accepted")
		}
	}
	if err := s.Authorize(strings.Repeat("a", 64), "host-a", status.Epoch, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(strings.Repeat("a", 64), "host-b", status.Epoch, true); err == nil {
		t.Fatal("other host accepted")
	}
	if err := s.Authorize(strings.Repeat("a", 64), "host-a", "old-epoch", true); err == nil {
		t.Fatal("old epoch accepted")
	}
}

func TestCompleteSnapshotPublishesOnlyVerifiedImmutableCopies(t *testing.T) {
	root, file, delivery := bootstrap(t)
	s, err := engines.New(filepath.Join(root, "runtime"), file, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry := deliver(t, delivery, "install-a", "1.0.0", "first")
	ack, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{entry}}))
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Ready || ack.AppliedRevision != 1 || len(s.Directory().Engines) != 1 {
		t.Fatal("snapshot not published")
	}
	lease, err := s.Acquire(entry.EngineID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	write(t, filepath.Join(delivery, "install-a", "package", "entry.js"), "changed source", 0600)
	data, err := os.ReadFile(lease.Entry)
	if err != nil || string(data) != "first" {
		t.Fatal("execution did not use private copy")
	}
}
func raw(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func write(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}
func deliver(t *testing.T, delivery, ref, version, content string) dto.EngineInstallation {
	t.Helper()
	node := filepath.Join(filepath.Dir(filepath.Dir(delivery)), "bin", "node")
	write(t, node, "#!/bin/sh\nprintf '%s' '{\"version\":\"22.22.0\",\"arch\":\""+runtime.GOARCH+"\"}'\n", 0700)
	pkg := filepath.Join(delivery, ref, "package")
	engine := dto.EngineResource{ID: "agent", Name: "Agent", Protocol: "acp", Transport: "stdio", Launch: dto.EngineLaunch{Runtime: "node", Entry: "entry.js", Args: []string{}}, Adapter: &dto.AdapterVersion{Package: "adapter", Version: "1.0.0", ACPSDKVersion: "2.0.2"}, Dependencies: []dto.EngineDependency{{ID: "node", Version: "22.22.0", Delivery: "host-managed"}}}
	manifest := dto.EnginePluginManifest{PluginAPIVersion: 2, ID: "test-plugin", Name: "Test", Description: "Fixture", Version: version, Platform: dto.PlatformTarget{Os: runtime.GOOS, Arch: runtime.GOARCH}}
	manifest.Desktop.BundleTopLevelDir = manifest.ID
	manifest.Requires.PlatformAgentEngineAPI = 2
	manifest.Requires.WebclientAgentEngineAPI = 1
	manifest.Resources.AgentEngines = []dto.EngineResource{engine}
	write(t, filepath.Join(pkg, "manifest.json"), string(raw(t, manifest)), 0600)
	write(t, filepath.Join(pkg, "entry.js"), content, 0600)
	write(t, filepath.Join(pkg, "node_modules", "adapter", "package.json"), `{"name":"adapter","version":"1.0.0"}`, 0600)
	write(t, filepath.Join(pkg, "node_modules", "@agentclientprotocol", "sdk", "package.json"), `{"name":"@agentclientprotocol/sdk","version":"2.0.2"}`, 0600)
	digest, err := connector.RuntimeFingerprint(pkg)
	if err != nil {
		t.Fatal(err)
	}
	record := dto.TrustedDeliveryRecord{SchemaVersion: 1, InstallationRef: ref, OwnerHostID: "host-a", PluginID: manifest.ID, PluginVersion: version, Platform: manifest.Platform, ArtifactDigest: "sha256:" + digest, Source: dto.DeliverySource{Kind: "local-import", AuthorizationRef: "local-import:" + ref, ArchiveDigest: "sha256:" + strings.Repeat("b", 64)}}
	write(t, filepath.Join(delivery, ref, "record.json"), string(raw(t, record)), 0600)
	return dto.EngineInstallation{EngineID: "test-plugin/agent", PluginID: manifest.ID, PluginVersion: version, InstallationRef: ref, ArtifactDigest: record.ArtifactDigest, Enabled: true}
}

func TestRevisionsAreAtomicIdempotentAndRequireInitialSnapshot(t *testing.T) {
	root, file, delivery := bootstrap(t)
	events := []dto.DirectoryChanged{}
	s, err := engines.New(filepath.Join(root, "runtime"), file, true, func(e dto.DirectoryChanged) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry := deliver(t, delivery, "install-a", "1.0.0", "first")
	upsert := map[string]any{}
	json.Unmarshal(raw(t, entry), &upsert)
	upsert["apiVersion"] = 1
	upsert["revision"] = 1
	if _, err := s.Apply("upsert", raw(t, upsert)); err == nil || err.Error() != "registry_not_ready" {
		t.Fatalf("initial delta: %v", err)
	}
	body := raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{entry}})
	if _, err := s.Apply("snapshot", body); err != nil {
		t.Fatal(err)
	}
	if ack, err := s.Apply("snapshot", body); err != nil || ack.Action != "idempotent" {
		t.Fatalf("retry: %+v %v", ack, err)
	}
	for _, revision := range []int64{1, 2} {
		_, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: revision, Entries: []dto.EngineInstallation{}}))
		if err == nil {
			t.Fatal("revision conflict/stale accepted")
		}
	}
	bad := entry
	bad.InstallationRef = "missing"
	bad.EngineID = "test-plugin/second"
	if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 3, Entries: []dto.EngineInstallation{entry, bad}})); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if s.Status().AppliedRevision != 2 || len(s.Directory().Engines) != 1 || len(events) != 1 {
		t.Fatal("failed or duplicate request changed directory")
	}
	if _, err := s.Apply("revoke", raw(t, dto.EngineRevoke{APIVersion: 1, Revision: 3, EngineID: entry.EngineID})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(entry.EngineID); err == nil {
		t.Fatal("revoked engine admitted")
	}
	if len(events) != 2 {
		t.Fatal("missing invalidation")
	}
}

func TestSnapshotsRemainLeasedAcrossUpgradeRevocationAndRestart(t *testing.T) {
	root, file, delivery := bootstrap(t)
	runtimeRoot := filepath.Join(root, "runtime")
	changes := 0
	s, err := engines.New(runtimeRoot, file, true, func(dto.DirectoryChanged) { changes++ })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entry := deliver(t, delivery, "install-a", "1.0.0", "first")
	apply := func(revision int64, entries []dto.EngineInstallation) {
		t.Helper()
		if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: revision, Entries: entries})); err != nil {
			t.Fatal(err)
		}
	}
	apply(1, []dto.EngineInstallation{entry})
	lease, err := s.Acquire(entry.EngineID)
	if err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatal("process acquisition did not invalidate directory state")
	}
	// A second registry/collector cannot remove the current directory or a live lease.
	observer, err := engines.New(runtimeRoot, "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if _, err := observer.Acquire(entry.EngineID); err == nil {
		t.Fatal("cache bypassed readiness")
	}
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Entry); err != nil {
		t.Fatal("current reference collected")
	}
	next := deliver(t, delivery, "install-b", "1.0.0", "same version, different bytes")
	apply(2, []dto.EngineInstallation{next})
	current, err := s.Acquire(next.EngineID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Entry == lease.Entry {
		t.Fatal("same-version upgrade overwrote snapshot")
	}
	apply(3, []dto.EngineInstallation{})
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Entry); err != nil {
		t.Fatal("active lease collected")
	}
	lease.Release()
	lease.Release()
	beforeRelease := changes
	current.Release()
	if changes != beforeRelease+1 {
		t.Fatal("process release did not invalidate directory state")
	}
	if _, err := os.Stat(lease.Entry); !os.IsNotExist(err) {
		t.Fatal("released obsolete snapshot was not collected")
	}
	if err := observer.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Entry); !os.IsNotExist(err) {
		t.Fatal("unused snapshot retained")
	}
}

func TestUntrustedImportsNeverChangePublishedDirectory(t *testing.T) {
	for _, scenario := range []string{"owner", "source", "digest", "dependency", "entry-directory", "nul-argv", "dependency-traversal", "unknown-execution", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, file, delivery := bootstrap(t)
			s, err := engines.New(filepath.Join(root, "runtime"), file, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			entry := deliver(t, delivery, "install-a", "1.0.0", "first")
			if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{entry}})); err != nil {
				t.Fatal(err)
			}
			bad := deliver(t, delivery, "install-b", "2.0.0", "second")
			pkg := filepath.Join(delivery, "install-b", "package")
			recordPath := filepath.Join(delivery, "install-b", "record.json")
			b, _ := os.ReadFile(recordPath)
			var record dto.TrustedDeliveryRecord
			json.Unmarshal(b, &record)
			manifestPath := filepath.Join(pkg, "manifest.json")
			b, _ = os.ReadFile(manifestPath)
			var manifest map[string]any
			json.Unmarshal(b, &manifest)
			engine := manifest["resources"].(map[string]any)["agentEngines"].([]any)[0].(map[string]any)
			switch scenario {
			case "owner":
				record.OwnerHostID = "other"
			case "source":
				record.Source.AuthorizationRef = "fake"
			case "digest":
				bad.ArtifactDigest = "sha256:" + strings.Repeat("0", 64)
			case "dependency":
				engine["dependencies"] = append(engine["dependencies"].([]any), map[string]any{"id": "missing", "version": "1.0.0", "delivery": "bundled", "path": "vendor/missing"})
			case "entry-directory":
				engine["launch"].(map[string]any)["entry"] = "node_modules"
			case "nul-argv":
				engine["launch"].(map[string]any)["args"] = []string{"bad\x00arg"}
			case "dependency-traversal":
				engine["dependencies"] = append(engine["dependencies"].([]any), map[string]any{"id": "escape", "version": "1.0.0", "delivery": "bundled", "path": "../outside"})
			case "unknown-execution":
				engine["launch"].(map[string]any)["env"] = map[string]string{"NODE_OPTIONS": "evil"}
			case "symlink":
				os.Remove(filepath.Join(pkg, "entry.js"))
				if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(pkg, "entry.js")); err != nil {
					t.Fatal(err)
				}
			}
			write(t, manifestPath, string(raw(t, manifest)), 0600)
			if scenario != "digest" && scenario != "symlink" {
				digest, err := connector.RuntimeFingerprint(pkg)
				if err != nil {
					t.Fatal(err)
				}
				record.ArtifactDigest = "sha256:" + digest
				bad.ArtifactDigest = record.ArtifactDigest
			}
			write(t, recordPath, string(raw(t, record)), 0600)
			if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{bad}})); err == nil {
				t.Fatal("untrusted import accepted")
			}
			if s.Status().AppliedRevision != 1 || s.Directory().Engines[0].PluginVersion != "1.0.0" {
				t.Fatal("failure replaced published state")
			}
		})
	}
}

func TestAdmissionRechecksSnapshotsAndBootstrapRejectsUnsafeAuthority(t *testing.T) {
	t.Run("snapshot replacement", func(t *testing.T) {
		root, file, delivery := bootstrap(t)
		s, err := engines.New(filepath.Join(root, "runtime"), file, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		entry := deliver(t, delivery, "install-a", "1.0.0", "first")
		if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 1, Entries: []dto.EngineInstallation{entry}})); err != nil {
			t.Fatal(err)
		}
		lease, err := s.Acquire(entry.EngineID)
		if err != nil {
			t.Fatal(err)
		}
		write(t, lease.Entry, "replaced", 0600)
		lease.Release()
		if _, err := s.Acquire(entry.EngineID); err == nil {
			t.Fatal("replaced snapshot admitted")
		}
		if _, err := s.Apply("snapshot", raw(t, dto.EngineSnapshot{APIVersion: 1, Revision: 2, Entries: []dto.EngineInstallation{entry}})); err == nil {
			t.Fatal("corrupt cached snapshot reused")
		}
	})
	for _, scenario := range []string{"remote-mode", "world-readable", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, file, _ := bootstrap(t)
			desktop := true
			switch scenario {
			case "remote-mode":
				desktop = false
			case "world-readable":
				os.Chmod(file, 0644)
			case "symlink":
				target := file + ".real"
				os.Rename(file, target)
				os.Symlink(target, file)
			}
			if s, err := engines.New(filepath.Join(root, "runtime"), file, desktop, nil); err == nil {
				s.Close()
				t.Fatal("unsafe bootstrap accepted")
			}
		})
	}
}

func TestDesktopDeliveryFingerprintVector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission vector; native Windows delivery verification remains T10")
	}
	root := t.TempDir()
	os.Chmod(root, 0750)
	write(t, filepath.Join(root, "a.txt"), "hello\n", 0640)
	write(t, filepath.Join(root, "bin", "run"), "node\n", 0755)
	os.Chmod(filepath.Join(root, "bin"), 0755)
	write(t, filepath.Join(root, "é.txt"), "中文\n", 0600)
	digest, err := connector.RuntimeFingerprint(root)
	if err != nil || digest != "6f3c0d80b57ab9657d99c62bcb6b375a1d78988710f2529a7814ee92f2a4b45e" {
		t.Fatalf("Desktop fingerprint mismatch: %s %v", digest, err)
	}
}
