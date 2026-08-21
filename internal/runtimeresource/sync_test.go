package runtimeresource

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSyncRegeneratesProviderAPIKeyFromPackagedRegistration(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "providers", "th-main.yml"), strings.Join([]string{
		"key: th-main",
		"baseUrl: https://old.example.com",
		"apiKey: old-runtime-key",
		"defaultModel: th-model",
	}, "\n"))

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Method != http.MethodPost {
			t.Errorf("method=%s", request.Method)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer package-grant" {
			t.Errorf("authorization=%q", authorization)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["name"] != "desktop-device-123" {
			t.Errorf("request body=%#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"new-generated-key"}`))
	}))
	defer server.Close()

	source := writeTestZip(t, providerRegistrationTestEntries(t, server.URL))
	result, err := Sync(Options{
		RuntimeDir:      runtimeRoot,
		Source:          source,
		DesktopFrom:     "1.0.0",
		DesktopTo:       "2.0.0",
		DesktopDeviceID: "desktop-device-123",
		Mode:            ModeVersionChange,
		Now:             fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || result.Stats.RegeneratedProviderKeys != 1 {
		t.Fatalf("requestCount=%d result=%#v", requestCount, result)
	}
	providerPath := filepath.Join(runtimeRoot, "registries", "providers", "th-main.yml")
	providerContent, err := os.ReadFile(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(providerContent), "apiKey: new-generated-key") || strings.Contains(string(providerContent), "old-runtime-key") {
		t.Fatalf("provider API key was not regenerated: %s", providerContent)
	}
	assertTestFile(t, filepath.Join(result.BackupDir, "registries", "providers", "th-main.yml"), strings.Join([]string{
		"key: th-main",
		"baseUrl: https://old.example.com",
		"apiKey: old-runtime-key",
		"defaultModel: th-model",
	}, "\n"))
	assertMissing(t, filepath.Join(runtimeRoot, providerRegisterFile))
}

func TestProviderRegistrationFailureDoesNotPublishOrCommit(t *testing.T) {
	runtimeRoot := t.TempDir()
	providerPath := filepath.Join(runtimeRoot, "registries", "providers", "th-main.yml")
	writeTestFile(t, providerPath, "key: th-main\napiKey: old-runtime-key\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := Sync(Options{
		RuntimeDir:      runtimeRoot,
		Source:          writeTestZip(t, providerRegistrationTestEntries(t, server.URL)),
		DesktopFrom:     "1.0.0",
		DesktopTo:       "2.0.0",
		DesktopDeviceID: "desktop-device-123",
		Mode:            ModeVersionChange,
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error=%v", err)
	}
	assertTestFile(t, providerPath, "key: th-main\napiKey: old-runtime-key\n")
	assertMissing(t, filepath.Join(runtimeRoot, stateDirectoryName, stateFileName))
	assertMissing(t, filepath.Join(runtimeRoot, stateDirectoryName, journalFileName))
}

func TestPackagedProviderRegistrationRequiresDesktopDeviceID(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requested = true
	}))
	defer server.Close()
	_, err := Sync(Options{
		RuntimeDir:  t.TempDir(),
		Source:      writeTestZip(t, providerRegistrationTestEntries(t, server.URL)),
		DesktopFrom: "1.0.0",
		DesktopTo:   "2.0.0",
		Mode:        ModeVersionChange,
	})
	if err == nil || !strings.Contains(err.Error(), "--desktop-device-id") {
		t.Fatalf("error=%v", err)
	}
	if requested {
		t.Fatal("provider registration request ran without a device id")
	}
}

func providerRegistrationTestEntries(t *testing.T, endpoint string) map[string]string {
	t.Helper()
	register, err := json.Marshal(map[string]any{
		"enabled":  true,
		"endpoint": endpoint,
		"grant": map[string]string{
			"type":  "jwt",
			"token": "package-grant",
		},
		"providers": []string{"th-main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"env/VERSION":                "v2.0.0\n",
		"env/provider-register.json": string(register),
		"env/registries/providers/th-main.yml": strings.Join([]string{
			"key: th-main",
			"baseUrl: https://new.example.com",
			"apiKey:",
			"defaultModel: th-model",
			"protocols:",
			"  OPENAI:",
			"    endpointPath: /v1/chat/completions",
		}, "\n"),
		"env/registries/models/th-model.yml": strings.Join([]string{
			"key: th-model",
			"provider: th-main",
			"protocol: OPENAI",
			"modelId: th-model",
		}, "\n"),
	}
}

func TestSyncOverwritesAllPackagedPlatformResourceDomains(t *testing.T) {
	runtimeRoot := t.TempDir()
	if os.PathSeparator == '/' {
		if err := os.Chmod(runtimeRoot, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(runtimeRoot, "VERSION"), "v1.9.0\n")
	for _, scope := range unitScopes {
		writeTestFile(t, filepath.Join(runtimeRoot, scope, "existing", "content.txt"), "user-"+scope)
		writeTestFile(t, filepath.Join(runtimeRoot, scope, "existing", "local-only.txt"), "local-"+scope)
		writeTestFile(t, filepath.Join(runtimeRoot, scope, "unknown", "content.txt"), "unknown-"+scope)
	}
	existingScript := filepath.Join(runtimeRoot, "agents", "existing", "keep-mode.sh")
	writeTestFile(t, existingScript, "#!/bin/sh\necho old\n")
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
	entries["env/agents/existing/keep-mode.sh"] = "#!/bin/sh\necho new\n"
	entries["env/agents/new/run.sh"] = "#!/bin/sh\n"
	entries["env/registries/authoritative.yml"] = "new-registry"
	source := writeTestZip(t, entries)
	result, err := Sync(Options{
		RuntimeDir:  runtimeRoot,
		Source:      source,
		DesktopFrom: "1.9.0",
		DesktopTo:   "2.0.0",
		Mode:        ModeVersionChange,
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Stats.AddedUnits != 4 || result.Stats.OverwrittenUnits != 4 ||
		result.Stats.PreservedUnits != 0 ||
		result.Stats.OverwrittenRegistryFiles != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, scope := range unitScopes {
		assertTestFile(t, filepath.Join(runtimeRoot, scope, "existing", "content.txt"), "package-"+scope)
		assertMissing(t, filepath.Join(runtimeRoot, scope, "existing", "local-only.txt"))
		assertTestFile(t, filepath.Join(runtimeRoot, scope, "new", "content.txt"), "new-"+scope)
		assertTestFile(t, filepath.Join(runtimeRoot, scope, "unknown", "content.txt"), "unknown-"+scope)
	}
	assertTestFile(t, existingScript, "#!/bin/sh\necho new\n")
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "new-registry")
	assertTestFile(t, filepath.Join(runtimeRoot, "registries", "user-extra.yml"), "user-registry")
	assertTestFile(t, filepath.Join(runtimeRoot, "VERSION"), "v2.0.0\n")
	if prefix := "1.9.0-to-2.0.0-"; !strings.HasPrefix(filepath.Base(result.BackupDir), prefix) {
		t.Fatalf("backup directory %q does not start with %q", result.BackupDir, prefix)
	}
	assertTestFile(t, filepath.Join(result.BackupDir, "VERSION"), "v1.9.0\n")
	assertTestFile(t, filepath.Join(result.BackupDir, "agents", "existing", "content.txt"), "user-agents")
	assertTestFile(t, filepath.Join(result.BackupDir, "agents", "existing", "local-only.txt"), "local-agents")
	state := readTestState(t, runtimeRoot)
	for _, wanted := range []string{
		"agents/existing", "agents/new",
		"skills-center/existing", "skills-center/new",
		"tools/existing", "tools/new",
		"teams/existing", "teams/new",
	} {
		if !slices.Contains(state.ManagedUnits, wanted) {
			t.Fatalf("state did not record %s: %#v", wanted, state.ManagedUnits)
		}
	}
	if os.PathSeparator == '/' {
		assertTestMode(t, runtimeRoot, 0o750)
		assertTestMode(t, existingScript, 0o755)
		assertTestMode(t, filepath.Join(runtimeRoot, "agents", "new", "run.sh"), 0o755)
	}
}

func TestSyncReplacesBundledUnitsAcrossFileAndDirectoryTypes(t *testing.T) {
	runtimeRoot := t.TempDir()
	writeTestFile(t, filepath.Join(runtimeRoot, "tools", "conflict", "user.txt"), "user-directory")
	writeTestFile(t, filepath.Join(runtimeRoot, "teams", "conflict"), "user-file")

	result, err := Sync(Options{
		RuntimeDir: runtimeRoot,
		Source: writeTestZip(t, map[string]string{
			"env/VERSION":                    "v2\n",
			"env/tools/conflict":             "package-file",
			"env/teams/conflict/content.txt": "package-directory",
		}),
		DesktopFrom: "1",
		DesktopTo:   "2",
		Mode:        ModeVersionChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.OverwrittenUnits != 2 || result.Stats.AddedUnits != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	assertTestFile(t, filepath.Join(runtimeRoot, "tools", "conflict"), "package-file")
	assertTestFile(t, filepath.Join(runtimeRoot, "teams", "conflict", "content.txt"), "package-directory")
	state := readTestState(t, runtimeRoot)
	for _, wanted := range []string{"tools/conflict", "teams/conflict"} {
		if !slices.Contains(state.ManagedUnits, wanted) {
			t.Fatalf("state did not record %s: %#v", wanted, state.ManagedUnits)
		}
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

func TestSyncPreservesInvalidLocalAgentWithoutFailingUpgrade(t *testing.T) {
	runtimeRoot := t.TempDir()
	invalidAgent := strings.Join([]string{
		"key: desktopAssistant",
		"name: Desktop Assistant",
		"role: legacy local Agent",
		"modelConfig:",
		"  modelKey: legacy-model",
		"toolConfig:",
		"  tools:",
		"    - platform_config",
		"mode: REACT",
	}, "\n")
	agentPath := filepath.Join(runtimeRoot, "agents", "desktopAssistant", "agent.yml")
	writeTestFile(t, agentPath, invalidAgent)

	result, err := Sync(Options{
		RuntimeDir:  runtimeRoot,
		Source:      writeTestZip(t, map[string]string{"env/VERSION": "v2.0.0\n"}),
		DesktopFrom: "1.0.0",
		DesktopTo:   "2.0.0",
		Mode:        ModeVersionChange,
		Now:         fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("upgrade did not commit: %#v", result)
	}
	assertTestFile(t, agentPath, invalidAgent)
	state := readTestState(t, runtimeRoot)
	if state.DesktopVersion != "2.0.0" {
		t.Fatalf("desktop version=%q want %q", state.DesktopVersion, "2.0.0")
	}
}

func TestReadStateAcceptsV1StatsWithoutOverwrittenUnits(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), stateFileName)
	writeTestFile(t, statePath, strings.Join([]string{
		`{`,
		`  "schemaVersion": 1,`,
		`  "transactionId": "legacy-transaction",`,
		`  "desktopVersion": "1.0.0",`,
		`  "packageSha256": "legacy-sha",`,
		`  "completedAt": "2026-08-14T00:00:00Z",`,
		`  "managedUnits": ["agents/managed"],`,
		`  "managedRegistryFiles": ["registries/models/managed.yml"],`,
		`  "stats": {"addedUnits": 1, "preservedUnits": 2}`,
		`}`,
	}, "\n"))

	state, exists, err := readState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || state.Stats.AddedUnits != 1 || state.Stats.PreservedUnits != 2 || state.Stats.OverwrittenUnits != 0 {
		t.Fatalf("unexpected legacy state: exists=%v state=%#v", exists, state)
	}
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

	writeTestFile(t, filepath.Join(runtimeRoot, "teams", "manual", "content.txt"), "old-manual")
	writeTestFile(t, filepath.Join(runtimeRoot, "teams", "manual", "local-only.txt"), "remove-me")
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
	if manualResult.Stats.OverwrittenUnits != 1 {
		t.Fatalf("manual import did not overwrite the packaged unit: %#v", manualResult)
	}
	assertTestFile(t, filepath.Join(runtimeRoot, "teams", "manual", "content.txt"), "manual")
	assertMissing(t, filepath.Join(runtimeRoot, "teams", "manual", "local-only.txt"))
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
	writeTestFile(t, filepath.Join(runtimeRoot, "VERSION"), "v1\n")
	writeTestFile(t, filepath.Join(runtimeRoot, "registries", "authoritative.yml"), "old")
	existingScript := filepath.Join(runtimeRoot, "agents", "existing", "rollback.sh")
	writeTestFile(t, existingScript, "#!/bin/sh\necho old\n")
	writeTestFile(t, filepath.Join(runtimeRoot, "agents", "existing", "local-only.txt"), "restore-me")
	if err := os.Chmod(existingScript, 0o710); err != nil {
		t.Fatal(err)
	}
	source := writeTestZip(t, map[string]string{
		"env/agents/existing/rollback.sh":  "#!/bin/sh\necho new\n",
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
	assertTestFile(t, existingScript, "#!/bin/sh\necho old\n")
	assertTestFile(t, filepath.Join(runtimeRoot, "agents", "existing", "local-only.txt"), "restore-me")
	assertTestFile(t, filepath.Join(runtimeRoot, "VERSION"), "v1\n")
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
