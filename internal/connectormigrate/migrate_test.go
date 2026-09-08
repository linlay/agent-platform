package connectormigrate

import (
	"agent-platform/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/mcp"
)

func TestMigrationPreservesCredentialsOutsidePackageAndSwitchesAgent(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "registries", "mcp-servers")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "search.yml"), []byte("serverKey: Search\nbaseUrl: https://example.test\nauthToken: private-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "agents", "demo")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentDir, "agent.yml")
	data := "key: demo\n# keep this comment\ntoolConfig:\n  tools: [bash]\n  mcp-servers:\n    - Search\nruntimeConfig:\n  env:\n    UNCHANGED: ${UNCHANGED}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := Run(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Applied || len(preview.Connectors) != 1 || len(preview.Agents) != 1 {
		t.Fatalf("bad preview %#v", preview)
	}
	if got, _ := os.ReadFile(path); string(got) != data {
		t.Fatal("preview changed agent")
	}
	result, err := Run(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.BackupDir == "" {
		t.Fatalf("bad result %#v", result)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy registry still present")
	}
	converted, _ := os.ReadFile(path)
	if strings.Contains(string(converted), "mcp-servers") || !strings.Contains(string(converted), "connectorConfig:\n  connectors:\n    - search") || !strings.Contains(string(converted), "UNCHANGED: ${UNCHANGED}") {
		t.Fatalf("agent conversion %s", converted)
	}
	pkg, err := connector.Load(filepath.Join(root, "connectors-center"), "search")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.AuthMode != "token" {
		t.Fatal("credential mode lost")
	}
	for _, file := range []string{"connector.json", "mcp.json"} {
		bytes, _ := os.ReadFile(filepath.Join(pkg.Dir, file))
		if strings.Contains(string(bytes), "private-value") {
			t.Fatal("secret in package")
		}
	}
	registry, err := mcp.NewRegistryWithSources(connector.Sources{ExternalRoot: filepath.Join(root, "connectors-center"), StateRoot: filepath.Join(root, "connector-state")})
	if err != nil {
		t.Fatal(err)
	}
	server, ok := registry.Server("search")
	if !ok || server.ResolvedURL() != "https://example.test/mcp" || server.Headers["Authorization"] != "Bearer private-value" || server.SetupError != "" {
		t.Fatal("migrated server lost URL or credentials")
	}
	if again, err := Run(root, true); err != nil || again.Applied {
		t.Fatalf("migration is not idempotent: %#v %v", again, err)
	}
}

func TestMigrationRejectsCollisionWithoutChangingSources(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "registries", "mcp-servers")
	_ = os.MkdirAll(legacy, 0o700)
	_ = os.WriteFile(filepath.Join(legacy, "demo.yml"), []byte("key: demo\nbaseUrl: https://example.test\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, "connectors", "demo"), 0o700)
	if _, err := Run(root, true); err == nil {
		t.Fatal("collision accepted")
	}
	if _, err := os.Stat(filepath.Join(legacy, "demo.yml")); err != nil {
		t.Fatal("collision modified legacy source")
	}
}

func TestMigrationKeepsIgnoredExampleInBackupOnly(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "registries", "mcp-servers")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	example := []byte("invalid: [example is deliberately not loadable\n")
	if err := os.WriteFile(filepath.Join(legacy, "imagine.example.yml"), example, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "search.yml"), []byte("key: search\nbaseUrl: https://example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []bool{false, true} {
		result, err := Run(root, apply)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Connectors) != 1 || result.Connectors[0] != "search" {
			t.Fatalf("example was activated: %#v", result)
		}
		if apply {
			data, err := os.ReadFile(filepath.Join(result.BackupDir, "registries", "mcp-servers", "imagine.example.yml"))
			if err != nil || string(data) != string(example) {
				t.Fatalf("example backup lost: %v", err)
			}
		}
	}
}

func TestMigrationMovesBuiltinSkillsAndCopiesWithoutLegacyMCP(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents", "demo")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "key: demo\n# retain outside comments\nskillConfig:\n  skills:\n    - ordinary\n    - builtin-httpx\n    - builtin-dbx\n  custom: value\nconnectorConfig:\n  connectors:\n    - builtin.httpx\nruntimeConfig:\n  env:\n    KEEP: ${UNCHANGED}\n"
	path := filepath.Join(agentDir, "agent.yml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"connectors/builtin.dbx", "connectors/builtin.httpx", "skills-center/builtin-dbx", "skills-center/builtin-httpx", "connectors/.builtin-state"} {
		dir := filepath.Join(root, filepath.FromSlash(scope))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "preserved"), []byte(scope), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := Run(root, false)
	if err != nil || preview.Applied || len(preview.Agents) != 1 || len(preview.Retired) != 6 {
		t.Fatalf("preview: %#v %v", preview, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != original {
		t.Fatalf("preview modified source: %v", err)
	}
	result, err := Run(root, true)
	if err != nil || !result.Applied {
		t.Fatalf("apply: %#v %v", result, err)
	}
	converted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := config.LoadYAMLTreeBytes(converted)
	if err != nil {
		t.Fatal(err)
	}
	def := tree.(map[string]any)
	skills := def["skillConfig"].(map[string]any)
	if !reflect.DeepEqual(skills["skills"], []any{"ordinary"}) || skills["custom"] != "value" {
		t.Fatalf("skills: %#v", skills)
	}
	mounts := def["connectorConfig"].(map[string]any)["connectors"]
	if !reflect.DeepEqual(mounts, []any{"builtin.httpx", "builtin.dbx"}) {
		t.Fatalf("mounts: %#v", mounts)
	}
	if !strings.Contains(string(converted), "# retain outside comments") || !strings.Contains(string(converted), "KEEP: ${UNCHANGED}") {
		t.Fatalf("unrelated bytes changed: %s", converted)
	}
	for _, scope := range result.Retired {
		if scope == "connectors" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(scope))); !os.IsNotExist(err) {
			t.Fatalf("retired copy retained: %s %v", scope, err)
		}
		data, err := os.ReadFile(filepath.Join(result.BackupDir, filepath.FromSlash(scope), "preserved"))
		if err != nil || string(data) != scope {
			t.Fatalf("backup lost: %s %v", scope, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(result.BackupDir, "agents", "demo", "agent.yml")); err != nil || string(data) != original {
		t.Fatalf("agent backup lost: %v", err)
	}
	if result, err := Run(root, true); err != nil || result.Applied {
		t.Fatalf("repeat: %#v %v", result, err)
	}
}

func TestBuiltinSkillMigrationPreservesOtherListsAndRejectsInlineParent(t *testing.T) {
	for _, data := range []string{
		"key: demo\nskillConfig:\n  skills:\n  - builtin-dbx\n  - ordinary\nruntimeConfig:\n  env:\n    KEEP: unchanged\n",
		"key: demo\nskillConfig:\n  skills:\n    - builtin-dbx\n  other:\n    - untouched\n",
	} {
		got, changed, err := migrateBuiltinSkills([]byte(data))
		if err != nil || !changed {
			t.Fatalf("migration: %s %v", got, err)
		}
		if _, err := config.LoadYAMLTreeBytes(got); err != nil {
			t.Fatalf("invalid YAML: %s %v", got, err)
		}
	}
	data := []byte("key: demo\nskillConfig: {skills: builtin-dbx}\n")
	if _, _, err := migrateBuiltinSkills(data); err == nil {
		t.Fatal("unsupported inline parent accepted")
	}
}
