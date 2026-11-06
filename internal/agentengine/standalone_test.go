package agentengine_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"testing"

	engines "agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
)

func standaloneArchive(t *testing.T, version string, ids ...string) *bytes.Reader {
	t.Helper()
	manifest := dto.EnginePluginManifest{PluginAPIVersion: 2, ID: "fixture", Name: "Fixture", Version: version, Description: "Test engine", Platform: dto.PlatformTarget{Os: runtime.GOOS, Arch: runtime.GOARCH}}
	if len(ids) > 0 {
		manifest.ID = ids[0]
	}
	manifest.Desktop.BundleTopLevelDir = manifest.ID
	manifest.Requires.PlatformAgentEngineAPI = 2
	manifest.Requires.WebclientAgentEngineAPI = 1
	manifest.Resources.AgentEngines = []dto.EngineResource{{ID: "agent", Name: "Fixture", Protocol: "acp", Transport: "stdio", Launch: dto.EngineLaunch{Runtime: "executable", Entry: "bin/agent.exe", Args: []string{}}, Dependencies: []dto.EngineDependency{}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range map[string][]byte{manifest.ID + "/manifest.json": data, manifest.ID + "/bin/agent.exe": []byte("fixture " + version)} {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0755)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestStandaloneInstallRestoresDirectoryWithoutDesktop(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	service, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Restore(); err != nil {
		t.Fatal(err)
	}
	archive := standaloneArchive(t, "1.0.0")
	if _, err = service.InstallArchive(archive, archive.Size(), false); err != nil {
		t.Fatal(err)
	}
	if got := service.Directory(); !got.Ready || len(got.Engines) != 1 || !got.Engines[0].Enabled {
		t.Fatalf("directory: %+v", got)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.Restore(); err != nil {
		t.Fatal(err)
	}
	lease, err := restarted.Acquire("fixture/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	data, err := os.ReadFile(lease.Entry)
	if err != nil || string(data) != "fixture 1.0.0" {
		t.Fatalf("restored program: %q %v", data, err)
	}
}

func TestStandaloneUpdateDisableAndUninstallDrainOnlyTarget(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	service, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Restore(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fixture", "other"} {
		archive := standaloneArchive(t, "1.0.0", id)
		if _, err = service.InstallArchive(archive, archive.Size(), false); err != nil {
			t.Fatal(err)
		}
	}
	old, err := service.Acquire("fixture/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	other, err := service.Acquire("other/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	archive := standaloneArchive(t, "2.0.0")
	if _, err = service.InstallArchive(archive, archive.Size(), true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.Retired:
	default:
		t.Fatal("old version admission remained open")
	}
	current, err := service.Acquire("fixture/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	if current.PluginVersion != "2.0.0" {
		t.Fatal("new version not published")
	}
	if _, err = service.ChangePlugin("fixture", "uninstall"); err != engines.Error("engine_busy") {
		t.Fatalf("active uninstall: %v", err)
	}
	if _, err = service.Acquire("fixture/agent"); err != engines.Error("engine_unavailable") {
		t.Fatalf("draining admission: %v", err)
	}
	select {
	case <-other.Retired:
		t.Fatal("other plugin interrupted")
	default:
	}
	if _, err = os.Stat(old.Entry); err != nil {
		t.Fatal("leased old version removed")
	}
	old.Release()
	current.Release()
	if _, err = service.ChangePlugin("fixture", "uninstall"); err != nil {
		t.Fatal(err)
	}
	directory := service.Directory()
	if len(directory.Engines) != 1 || directory.Engines[0].PluginID != "other" {
		t.Fatalf("directory=%+v", directory)
	}
}

func TestStandaloneRejectsInvalidUpgradeWithoutChangingInstalledState(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	service, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Restore(); err != nil {
		t.Fatal(err)
	}
	archive := standaloneArchive(t, "1.0.0")
	if _, err = service.InstallArchive(archive, archive.Size(), false); err != nil {
		t.Fatal(err)
	}
	before := service.Directory().Revision
	service.SetValidator(func(_ string, lease *engines.Lease) error {
		defer lease.Release()
		return engines.Error("protocol_unsupported")
	})
	upgrade := standaloneArchive(t, "2.0.0")
	if _, err = service.InstallArchive(upgrade, upgrade.Size(), true); err == nil {
		t.Fatal("invalid protocol accepted")
	}
	if service.Directory().Revision != before {
		t.Fatal("failed upgrade changed directory")
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.Restore(); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Directory().Engines[0].PluginVersion; got != "1.0.0" {
		t.Fatalf("failed upgrade persisted: %s", got)
	}
}

func TestStandaloneRejectsUnsafeArchivesAndConcurrentOwner(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	service, err := engines.NewStandalone(root, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if second, err := engines.NewStandalone(root, "", nil); err != engines.Error("engine_busy") {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second owner: %v", err)
	}
	if err = service.Restore(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape", "/absolute", "fixture/../escape", "fixture\\escape"} {
		var buf bytes.Buffer
		writer := zip.NewWriter(&buf)
		file, _ := writer.Create(name)
		file.Write([]byte("data"))
		writer.Close()
		if _, err := service.InstallArchive(bytes.NewReader(buf.Bytes()), int64(buf.Len()), false); err == nil {
			t.Fatalf("unsafe archive accepted: %s", name)
		}
	}
	if len(service.Directory().Engines) != 0 {
		t.Fatal("unsafe archive published")
	}
}
