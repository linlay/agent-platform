package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func acpStoreFixture(t *testing.T, original string) (*ACPRegistrationStore, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "agent-settings.yml")
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := cfg.applyAgentSettingsFile(file); err != nil {
		t.Fatal(err)
	}
	return NewACPRegistrationStore(cfg.ACP), file
}
func acpTestInput(id string) ACPRegistration {
	return ACPRegistration{SourcePluginID: "plugin-" + id, BridgeID: id, BaseURL: "http://127.0.0.1:17071", TimeoutMS: 300000}
}
func readACPFile(t *testing.T, file string) string {
	t.Helper()
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func requireACP(t *testing.T, store *ACPRegistrationStore, input ACPRegistration, remove bool) ACPRegistrationResult {
	t.Helper()
	result, err := store.Mutate(input, remove)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestACPRegistrationLifecycle(t *testing.T) {
	original := "# preserve defaults\ncoder:\n  default-agent:\n    modelKey: native\nacp-bridges:\n  manual:\n    base-url: https://example.test\ngeneral:\n  workspace-agents:\n    file: AGENTS.md\n"
	store, file := acpStoreFixture(t, original)
	input := acpTestInput("codex")
	token := "test-secret"
	input.AuthToken = &token
	result := requireACP(t, store, input, false)
	if !result.Changed || !result.RestartRequired {
		t.Fatalf("%+v", result)
	}
	first := readACPFile(t, file)
	if !strings.Contains(first, "# preserve defaults\ncoder:\n  default-agent:\n    modelKey: native") || !strings.Contains(first, "workspace-agents:\n    file: AGENTS.md") {
		t.Fatal("unrelated source changed")
	}
	input.AuthToken = nil
	result = requireACP(t, store, input, false)
	if result.Changed || !result.RestartRequired || readACPFile(t, file) != first {
		t.Fatal("retry must retain pending restart and token without writing")
	}
	wrong := input
	wrong.SourcePluginID = "other"
	for _, remove := range []bool{false, true} {
		if _, err := store.Mutate(wrong, remove); !errors.Is(err, ErrACPConflict) {
			t.Fatal(err)
		}
	}
	if readACPFile(t, file) != first {
		t.Fatal("conflicting owner changed file")
	}
	var loaded Config
	if err := loaded.applyAgentSettingsFile(file); err != nil {
		t.Fatal(err)
	}
	if loaded.ACP.ACPBridges["codex"].AuthToken != token {
		t.Fatal("token lost")
	}
	restarted := NewACPRegistrationStore(loaded.ACP)
	if result = requireACP(t, restarted, input, false); result.Changed || result.RestartRequired {
		t.Fatal(result)
	}
	result = requireACP(t, restarted, input, true)
	if !result.Changed || !result.Removed || !result.RestartRequired {
		t.Fatal(result)
	}
	if result = requireACP(t, restarted, input, true); result.Changed || result.Removed || !result.RestartRequired {
		t.Fatal(result)
	}
	if !strings.Contains(readACPFile(t, file), "manual:") {
		t.Fatal("deleted unrelated entry")
	}
}
func TestACPRegistrationLegacyAndValidation(t *testing.T) {
	store, file := acpStoreFixture(t, "acp-bridges:\n  codex:\n    base-url: http://127.0.0.1:17071\n    extra: kept\n")
	input := acpTestInput("codex")
	before := readACPFile(t, file)
	if _, err := store.Mutate(input, true); !errors.Is(err, ErrACPConflict) {
		t.Fatal(err)
	}
	changed := input
	changed.BaseURL = "http://127.0.0.1:18080"
	if _, err := store.Mutate(changed, false); !errors.Is(err, ErrACPConflict) {
		t.Fatal(err)
	}
	if readACPFile(t, file) != before {
		t.Fatal("unmanaged config was changed")
	}
	if result := requireACP(t, store, input, false); !result.Changed || result.RestartRequired {
		t.Fatal(result)
	}
	if !strings.Contains(readACPFile(t, file), "extra: kept") {
		t.Fatal("unknown field lost")
	}
	for _, raw := range []string{"acp-proxies: {}\n", "acp-bridges: []\n", "acp-bridges: {}\nacp-bridges: {}\n", "acp-bridges:\n  codex: 4\n"} {
		if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Mutate(input, false); !errors.Is(err, ErrACPConfig) {
			t.Fatal(err)
		}
		if readACPFile(t, file) != raw {
			t.Fatal("invalid YAML rewritten")
		}
	}
}
func TestACPRegistrationConcurrentAndWriteFailure(t *testing.T) {
	store, file := acpStoreFixture(t, "acp-bridges: {}\n")
	var wg sync.WaitGroup
	for _, id := range []string{"codex", "claude", "other"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := store.Mutate(acpTestInput(id), false); err != nil {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	var cfg Config
	if err := cfg.applyAgentSettingsFile(file); err != nil {
		t.Fatal(err)
	}
	if len(cfg.ACP.ACPBridges) != 3 {
		t.Fatal("concurrent update lost")
	}
	// A non-directory parent deterministically prevents publication on all OSes.
	original := readACPFile(t, file)
	store.path = filepath.Join(file, "agent-settings.yml")
	if _, err := store.Mutate(acpTestInput("new"), false); err == nil {
		t.Fatal("expected failure")
	}
	if readACPFile(t, file) != original {
		t.Fatal("existing config was damaged")
	}
	store.path = file
	if result := requireACP(t, store, acpTestInput("new"), false); !result.Changed {
		t.Fatal(result)
	}
}
func TestACPRegistrationMissingFileAndUnsafeInputs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "agent-settings.yml")
	store := NewACPRegistrationStore(ACPSettingsConfig{SourcePath: file})
	input := acpTestInput("codex")
	for _, url := range []string{"file:///tmp/a", "http://user:secret@example.test", "http://"} {
		bad := input
		bad.BaseURL = url
		if _, err := store.Mutate(bad, false); !errors.Is(err, ErrACPArguments) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("invalid requests wrote config")
	}
	requireACP(t, store, input, false)
	if result := requireACP(t, store, input, true); !result.Removed || result.RestartRequired {
		t.Fatal(result)
	}
	requireACP(t, store, input, false)
	token := "${UNSET_ACP_TEST_SECRET:fallback}"
	input.AuthToken = &token
	before := readACPFile(t, file)
	if _, err := store.Mutate(input, false); !errors.Is(err, ErrACPArguments) {
		t.Fatal(err)
	}
	if readACPFile(t, file) != before {
		t.Fatal("unsafe token written")
	}
}

func TestACPRegistrationAtomicPublicationFailure(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "agent-settings.yml")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "untouched")
	if err := os.WriteFile(sentinel, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeACPSettings(target, []byte("candidate")); err == nil {
		t.Fatal("expected replacement failure")
	}
	if readACPFile(t, sentinel) != "original" {
		t.Fatal("failed publication damaged destination")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 {
		t.Fatalf("staged file leaked: %v %v", files, err)
	}
}
