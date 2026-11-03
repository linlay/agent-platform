package runtimerecovery

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/automation"
	"agent-platform/internal/chat"
)

func fixture(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	options := Options{Source: filepath.Join(root, "source"), Target: filepath.Join(root, "candidate"), Action: "restore"}
	for _, dir := range []string{options.Source, options.Target} {
		write(t, filepath.Join(dir, "VERSION"), "v"+SupportedVersion)
	}
	store, err := chat.NewFileStoreAtStartup(filepath.Join(options.Source, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return options
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRestoreCurrentVersionPreservesStoresAndUserResources(t *testing.T) {
	o := fixture(t)
	write(t, filepath.Join(o.Source, "agents/builtin/agent.yml"), "old")
	write(t, filepath.Join(o.Target, "agents/builtin/agent.yml"), "release")
	write(t, filepath.Join(o.Source, "agents/custom/agent.yml"), "custom")
	write(t, filepath.Join(o.Source, "registries/models/user.yml"), "user")
	write(t, filepath.Join(o.Target, "registries/models/seed.yml"), "seed")
	write(t, filepath.Join(o.Source, "chats/chat-a/upload.txt"), "attachment")
	write(t, filepath.Join(o.Source, "chats/chat-a/upload.jsonl"), "arbitrary user upload")
	write(t, filepath.Join(o.Source, "chats/chat-a.jsonl"), `{"_type":"query","chatId":"chat-a","runId":"run-a","updatedAt":1700000000001,"query":{"role":"user","message":"hello"}}`)
	write(t, filepath.Join(o.Source, "memory/user.md"), "memory")
	write(t, filepath.Join(o.Source, ".state/connectors/test/token"), "credential")
	write(t, filepath.Join(o.Source, ".desktop/config/services/agent-platform/.env"), "broken-config")
	write(t, filepath.Join(o.Source, "ru-agents/old/generated"), "stale-cache")
	dbBefore := read(t, filepath.Join(o.Source, "chats/chats.db"))
	result, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != SupportedVersion {
		t.Fatalf("unexpected report: %#v", result)
	}
	for path, value := range map[string]string{
		"agents/builtin/agent.yml": "release", "agents/custom/agent.yml": "custom", "registries/models/user.yml": "user",
		"registries/models/seed.yml": "seed", "chats/chat-a/upload.txt": "attachment", "memory/user.md": "memory", ".state/connectors/test/token": "credential",
		"chats/chat-a/upload.jsonl": "arbitrary user upload",
	} {
		if got := string(read(t, filepath.Join(o.Target, path))); got != value {
			t.Fatalf("%s: %q", path, got)
		}
	}
	for _, excluded := range []string{".desktop", "ru-agents"} {
		if _, err := os.Stat(filepath.Join(o.Target, excluded)); !os.IsNotExist(err) {
			t.Fatalf("restored excluded scope %s", excluded)
		}
	}
	if !bytes.Equal(dbBefore, read(t, filepath.Join(o.Source, "chats/chats.db"))) {
		t.Fatal("source DB was modified")
	}
	if !bytes.Equal(dbBefore, read(t, filepath.Join(o.Target, "chats/chats.db"))) {
		t.Fatal("target DB changed")
	}
	if _, err := Run(o); err != nil {
		t.Fatalf("completed retry: %v", err)
	}
}

func TestRejectsUnapprovedVersionsAndSchemasBeforeCopy(t *testing.T) {
	for _, kind := range []string{"version", "schema", "unmarked", "symlink", "interrupted"} {
		t.Run(kind, func(t *testing.T) {
			o := fixture(t)
			write(t, filepath.Join(o.Source, "memory/data"), "original")
			switch kind {
			case "version":
				write(t, filepath.Join(o.Source, "VERSION"), "0.5.29")
			case "schema", "unmarked":
				db, err := sql.Open("sqlite", filepath.Join(o.Source, "chats/chats.db"))
				if err != nil {
					t.Fatal(err)
				}
				statement := "PRAGMA user_version=99"
				if kind == "unmarked" {
					statement = "PRAGMA user_version=0; PRAGMA application_id=0"
				}
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				db.Close()
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(o.Source, "memory/link")); err != nil {
					t.Skip(err)
				}
			case "interrupted":
				write(t, filepath.Join(o.Target, ".platform-recovery-pending"), "")
			}
			if _, err := Run(o); err == nil {
				t.Fatal("unsupported input accepted")
			}
			if _, err := os.Stat(filepath.Join(o.Target, "memory")); !os.IsNotExist(err) {
				t.Fatal("candidate was mutated")
			}
			if string(read(t, filepath.Join(o.Source, "memory/data"))) != "original" {
				t.Fatal("source was changed")
			}
		})
	}
}

func TestRestorePausesAutomationsWithoutChangingSource(t *testing.T) {
	o := fixture(t)
	definition := "name: Test\nenabled: true\ncron: '0 9 * * *'\nagentKey: demo\nquery:\n  message: do work\n"
	write(t, filepath.Join(o.Source, "automations/daily.yml"), definition)
	result, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := automation.NewRegistry(filepath.Join(o.Target, "automations")).Load()
	if err != nil || len(defs) != 1 || defs[0].Enabled || result.PausedAutomations != 1 {
		t.Fatalf("not paused: %#v %v %v", defs, result, err)
	}
	if string(read(t, filepath.Join(o.Source, "automations/daily.yml"))) != definition {
		t.Fatal("source definition changed")
	}
}

func TestCheckDoesNotWriteAndRejectsOverlap(t *testing.T) {
	o := fixture(t)
	o.Action = "check"
	if _, err := Run(o); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(o.Target)
	if len(entries) != 1 || entries[0].Name() != "VERSION" {
		t.Fatal("check wrote to candidate")
	}
	o.Target = o.Source
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "separate") {
		t.Fatalf("overlap accepted: %v", err)
	}
}

func TestFixedLayoutConfigIsCheckedWithoutEvaluatingShell(t *testing.T) {
	for _, custom := range []bool{false, true} {
		o := fixture(t)
		o.Action = "check"
		o.SourceConfigDir = t.TempDir()
		value := "AP_RUNTIME_DIR='" + o.Source + "'\nAP_RUNTIME_STATE_DIR=\nAP_RUNTIME_CHATS_DIR='" + filepath.Join(o.Source, "chats") + "'\n"
		if custom {
			value += "AP_RUNTIME_MEMORY_DIR=/external/memory\n"
		}
		write(t, filepath.Join(o.SourceConfigDir, ".env"), value)
		_, err := Run(o)
		if custom && (err == nil || !strings.Contains(err.Error(), "AP_RUNTIME_MEMORY_DIR")) {
			t.Fatalf("custom root was not rejected: %v", err)
		}
		if !custom && err != nil {
			t.Fatal(err)
		}
		if string(read(t, filepath.Join(o.SourceConfigDir, ".env"))) != value {
			t.Fatal("configuration was rewritten")
		}
	}
}
