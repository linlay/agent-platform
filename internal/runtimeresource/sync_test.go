package runtimeresource

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSyncMergesAllPlatformResourceDomains(t *testing.T) {
	runtimeRoot := t.TempDir()
	if os.PathSeparator == '/' {
		if err := os.Chmod(runtimeRoot, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range unitScopes {
		writeTestFile(t, filepath.Join(runtimeRoot, scope, "existing", "content.txt"), "user-"+scope)
	}
	existingScript := filepath.Join(runtimeRoot, "agents", "existing", "keep-mode.sh")
	writeTestFile(t, existingScript, "#!/bin/sh\n")
	if err := os.Chmod(existingScript, 0o640); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "old-registry")
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "user-extra.yml"), "user-registry")

	entries := map[string]string{"env/VERSION": "v2.0.0\n"}
	for _, scope := range unitScopes {
		entries["env/"+scope+"/existing/content.txt"] = "package-" + scope
		entries["env/"+scope+"/new/content.txt"] = "new-" + scope
	}
	entries["env/agents/new/run.sh"] = "#!/bin/sh\n"
	entries["env/registries/authoritative.yml"] = "new-registry"
	source := writeTestZip(t, entries)
	result, err := Sync(Options{
		RuntimeDir:  runtimeRoot,
		Source:      source,
		DesktopFrom: "legacy",
		DesktopTo:   "2.0.0",
		Mode:        ModeVersionChange,
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Stats.AddedUnits != 4 || result.Stats.PreservedUnits != 4 ||
		result.Stats.OverwrittenRegistryFiles != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, scope := range unitScopes {
		assertTestFile(t, filepath.Join(runtimeRoot, scope, "existing", "content.txt"), "user-"+scope)
		assertTestFile(t, filepath.Join(runtimeRoot, scope, "new", "content.txt"), "new-"+scope)
	}
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "new-registry")
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "user-extra.yml"), "user-registry")
	state := readTestState(t, runtimeRoot)
	for _, wanted := range []string{"agents/new", "skills-center/new", "tools/new", "teams/new"} {
		if !slices.Contains(state.ManagedUnits, wanted) {
			t.Fatalf("state did not record %s: %#v", wanted, state.ManagedUnits)
		}
	}
	if slices.Contains(state.ManagedUnits, "agents/existing") {
		t.Fatalf("user-owned colliding Agent was incorrectly claimed: %#v", state.ManagedUnits)
	}
	if os.PathSeparator == '/' {
		assertTestMode(t, runtimeRoot, 0o750)
		assertTestMode(t, existingScript, 0o640)
		assertTestMode(t, filepath.Join(runtimeRoot, "agents", "new", "run.sh"), 0o755)
	}
}

func TestSyncRemovesOnlyPreviouslyManagedResources(t *testing.T) {
	runtimeRoot := t.TempDir()
	previous := writeTestZipAt(t, filepath.Join(t.TempDir(), "previous.zip"), map[string]string{
		"env/VERSION":                 "v1.0.0\n",
		"env/agents/retired/data.txt": "managed",
		"env/registries/retired.yml":  "managed-registry",
	})
	writeTestFile(t, filepath.Join(runtimeRoot, "agents", "retired", "data.txt"), "managed-customized")
	writeTestFile(t, filepath.Join(runtimeRoot, "agents", "unknown", "data.txt"), "unknown")
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "retired.yml"), "managed-customized")
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "unknown.yml"), "unknown")
	current := writeTestZip(t, map[string]string{"env/VERSION": "v2.0.0\n"})

	result, err := Sync(Options{
		RuntimeDir:     runtimeRoot,
		Source:         current,
		PreviousSource: previous,
		DesktopFrom:    "1.0.0",
		DesktopTo:      "2.0.0",
		Mode:           ModeVersionChange,
		Now:            fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.RemovedManagedUnits != 1 || result.Stats.RemovedManagedRegistryFiles != 1 {
		t.Fatalf("unexpected removal stats: %#v", result.Stats)
	}
	assertMissing(t, filepath.Join(runtimeRoot, "agents", "retired"))
	assertMissing(t, filepath.Join(runtimeRoot, "registries", "retired.yml"))
	assertTestFile(t, filepath.Join(runtimeRoot, "agents", "unknown", "data.txt"), "unknown")
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "unknown.yml"), "unknown")
}

func TestVersionChangeIdempotencyIgnoresSourceSHA(t *testing.T) {
	runtimeRoot := t.TempDir()
	first := writeTestZip(t, map[string]string{
		"env/VERSION":                  "v2.0.0\n",
		"env/agents/first/content.txt": "first",
	})
	if _, err := Sync(Options{RuntimeDir: runtimeRoot, Source: first, DesktopFrom: "1", DesktopTo: "2.0.0", Mode: ModeVersionChange}); err != nil {
		t.Fatal(err)
	}
	result, err := Sync(Options{
		RuntimeDir:  runtimeRoot,
		Source:      filepath.Join(t.TempDir(), "does-not-exist.zip"),
		DesktopFrom: "2.0.0",
		DesktopTo:   "2.0.0",
		Mode:        ModeVersionChange,
	})
	if err != nil {
		t.Fatalf("same version unexpectedly read the source: %v", err)
	}
	if result.Changed {
		t.Fatalf("same version unexpectedly changed resources: %#v", result)
	}

	manual := writeTestZip(t, map[string]string{
		"env/VERSION":                  "v2.0.0\n",
		"env/teams/manual/content.txt": "manual",
	})
	manualResult, err := Sync(Options{RuntimeDir: runtimeRoot, Source: manual, DesktopFrom: "2.0.0", DesktopTo: "2.0.0", Mode: ModeManualImport})
	if err != nil {
		t.Fatal(err)
	}
	if !manualResult.Changed {
		t.Fatal("manual import should run at the same Desktop version")
	}
	assertTestFile(t, filepath.Join(runtimeRoot, "teams", "manual", "content.txt"), "manual")
}

func TestRegistryImageSchemaUsesActualModelLoader(t *testing.T) {
	validImage := strings.Join([]string{
		"key: image-model",
		"provider: mock",
		"type: image-generation",
		"modelId: image-model",
		"image:",
		"  generation:",
		"    endpointPath: /v1/images/generations",
		"    requestFormat: openai-images-json",
	}, "\n")
	base := map[string]string{
		"env/VERSION": "v2.0.0\n",
		"env/registries/providers/mock.yml": strings.Join([]string{
			"key: mock",
			"baseUrl: https://example.com",
			"apiKey: secret",
			"defaultModel: image-model",
		}, "\n"),
		"env/registries/models/image.yml": validImage,
	}
	if _, err := Sync(Options{RuntimeDir: t.TempDir(), Source: writeTestZip(t, base), DesktopFrom: "1", DesktopTo: "2.0.0", Mode: ModeVersionChange}); err != nil {
		t.Fatalf("new image schema was rejected: %v", err)
	}
	base["env/registries/models/image.yml"] = strings.Join([]string{
		"key: image-model",
		"provider: mock",
		"type: image-generation",
		"modelId: image-model",
		"image:",
		"  endpointPath: /v1/images/generations",
	}, "\n")
	_, err := Sync(Options{RuntimeDir: t.TempDir(), Source: writeTestZip(t, base), DesktopFrom: "1", DesktopTo: "2.0.0", Mode: ModeVersionChange})
	if err == nil || !strings.Contains(err.Error(), "image.endpointPath is no longer supported") {
		t.Fatalf("legacy image schema error=%v", err)
	}
}

func TestSyncRejectsUnsafeOrInvalidArchives(t *testing.T) {
	t.Run("path escape", func(t *testing.T) {
		source := writeTestZip(t, map[string]string{"env/VERSION": "v2\n", "../escape": "bad"})
		_, err := Sync(Options{RuntimeDir: t.TempDir(), Source: source, DesktopFrom: "1", DesktopTo: "2", Mode: ModeVersionChange})
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		sourcePath := filepath.Join(t.TempDir(), "symlink.zip")
		file, err := os.Create(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		version, _ := writer.Create("env/VERSION")
		_, _ = version.Write([]byte("v2\n"))
		header := &zip.FileHeader{Name: "env/agents/link"}
		header.SetMode(os.ModeSymlink | 0o777)
		link, _ := writer.CreateHeader(header)
		_, _ = link.Write([]byte("../../outside"))
		_ = writer.Close()
		_ = file.Close()
		_, err = Sync(Options{RuntimeDir: t.TempDir(), Source: sourcePath, DesktopFrom: "1", DesktopTo: "2", Mode: ModeVersionChange})
		if err == nil || !strings.Contains(err.Error(), "unsupported file type") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "corrupt.zip")
		writeTestFile(t, source, "not-a-zip")
		_, err := Sync(Options{RuntimeDir: t.TempDir(), Source: source, DesktopFrom: "1", DesktopTo: "2", Mode: ModeVersionChange})
		if err == nil || !strings.Contains(err.Error(), "open runtime resource ZIP") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("version mismatch", func(t *testing.T) {
		source := writeTestZip(t, map[string]string{"env/VERSION": "v3\n"})
		_, err := Sync(Options{RuntimeDir: t.TempDir(), Source: source, DesktopFrom: "1", DesktopTo: "2", Mode: ModeVersionChange})
		if err == nil || !strings.Contains(err.Error(), "VERSION mismatch") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("registry type conflict", func(t *testing.T) {
		runtimeRoot := t.TempDir()
		writeTestFile(t, filepath.Join(runtimeRoot, "registries", "conflict", "user.txt"), "user")
		source := writeTestZip(t, map[string]string{"env/VERSION": "v2\n", "env/registries/conflict": "file"})
		_, err := Sync(Options{RuntimeDir: runtimeRoot, Source: source, DesktopFrom: "1", DesktopTo: "2", Mode: ModeVersionChange})
		if err == nil || !strings.Contains(err.Error(), "file/directory conflict") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestPublishFailureRollsBackWithoutCommittingState(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "old")
	existingScript := filepath.Join(runtimeRoot, "agents", "existing", "rollback.sh")
	writeTestFile(t, existingScript, "#!/bin/sh\n")
	if err := os.Chmod(existingScript, 0o710); err != nil {
		t.Fatal(err)
	}
	source := writeTestZip(t, map[string]string{
		"env/VERSION":                      "v2\n",
		"env/registries/authoritative.yml": "new",
	})
	injected := errors.New("injected publish failure")
	_, err := Sync(Options{
		RuntimeDir:  runtimeRoot,
		Source:      source,
		DesktopFrom: "1",
		DesktopTo:   "2",
		Mode:        ModeVersionChange,
		AfterPublishStep: func(relative string) error {
			if relative == "registries" {
				return injected
			}
			return nil
		},
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error=%v", err)
	}
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "old")
	if os.PathSeparator == '/' {
		assertTestMode(t, existingScript, 0o710)
	}
	assertMissing(t, filepath.Join(runtimeRoot, stateDirectoryName, stateFileName))
	assertMissing(t, filepath.Join(runtimeRoot, stateDirectoryName, journalFileName))
}

func writeTestZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	return writeTestZipAt(t, filepath.Join(t.TempDir(), "env.zip"), entries)
}

func writeTestZipAt(t *testing.T, target string, entries map[string]string) string {
	t.Helper()
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	keys := sortedKeys(entries)
	for _, name := range keys {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(entries[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func writeTestFile(t *testing.T, target, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, target, expected string) {
	t.Helper()
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("%s=%q want %q", target, data, expected)
	}
}

func assertMissing(t *testing.T, target string) {
	t.Helper()
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be missing, err=%v", target, err)
	}
}

func assertTestMode(t *testing.T, target string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("%s mode=%#o want %#o", target, actual, expected)
	}
}

func readTestState(t *testing.T, runtimeRoot string) State {
	t.Helper()
	state, exists, err := readState(filepath.Join(runtimeRoot, stateDirectoryName, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("runtime resource state was not written")
	}
	return state
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
}
