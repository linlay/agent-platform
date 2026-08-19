package catalog

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

type agentArchiveTestEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func TestEditableAgentArchiveImportsCompleteRootAndWrappedPackages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configName string
		prefix     string
	}{
		{name: "root yml", configName: "agent.yml"},
		{name: "wrapped yaml", configName: "agent.yaml", prefix: "portable-agent/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentsDir := filepath.Join(t.TempDir(), "agents")
			registry := newAgentArchiveTestRegistry(agentsDir)
			archive := buildAgentImportZIP(t, []agentArchiveTestEntry{
				{name: tc.prefix + tc.configName, content: "key: portable-agent\nname: Portable Agent\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"},
				{name: tc.prefix + "SOUL.md", content: "Portable soul\n"},
				{name: tc.prefix + "skills/local-skill/SKILL.md", content: "---\nname: local-skill\n---\n"},
				{name: tc.prefix + ".config/token", content: "secret\n"},
				{name: tc.prefix + "knowledge/guide.md", content: "Guide\n"},
				{name: tc.prefix + ".DS_Store", content: "noise"},
				{name: "__MACOSX/._agent.yml", content: "noise"},
			})

			mutation, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
			if err != nil {
				t.Fatalf("BeginImportEditableAgentArchive returned error: %v", err)
			}
			if mutation.Key != "portable-agent" || mutation.Name != "Portable Agent" {
				t.Fatalf("unexpected mutation identity: %#v", mutation)
			}
			if err := registry.CommitEditableAgentArchiveMutation(mutation); err != nil {
				t.Fatalf("commit import: %v", err)
			}

			finalDir := filepath.Join(agentsDir, "portable-agent")
			for _, rel := range []string{tc.configName, "SOUL.md", "skills/local-skill/SKILL.md", ".config/token", "knowledge/guide.md"} {
				if _, err := os.Stat(filepath.Join(finalDir, filepath.FromSlash(rel))); err != nil {
					t.Fatalf("expected imported file %s: %v", rel, err)
				}
			}
			if _, err := os.Stat(filepath.Join(finalDir, ".DS_Store")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf(".DS_Store should be ignored, got %v", err)
			}
			configInfo, err := os.Stat(filepath.Join(finalDir, ".config"))
			if err != nil {
				t.Fatalf("stat .config: %v", err)
			}
			if configInfo.Mode().Perm() != 0o700 {
				t.Fatalf(".config mode = %o, want 700", configInfo.Mode().Perm())
			}
			tokenInfo, err := os.Stat(filepath.Join(finalDir, ".config", "token"))
			if err != nil {
				t.Fatalf("stat .config token: %v", err)
			}
			if tokenInfo.Mode().Perm() != 0o600 {
				t.Fatalf(".config token mode = %o, want 600", tokenInfo.Mode().Perm())
			}
		})
	}
}

func TestEditableAgentArchiveConflictOverwriteAndRollback(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	existingDir := filepath.Join(agentsDir, "existing")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatalf("mkdir existing agent: %v", err)
	}
	oldConfig := "key: existing\nname: Existing Agent\nmode: REACT\nmodelConfig:\n  modelKey: old-model\n"
	if err := os.WriteFile(filepath.Join(existingDir, "agent.yml"), []byte(oldConfig), 0o644); err != nil {
		t.Fatalf("write existing agent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "old-only.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old resource: %v", err)
	}
	registry := newAgentArchiveTestRegistry(agentsDir)
	registry.adminAgents["existing"] = AdminAgent{
		Key:    "existing",
		Name:   "Existing Agent",
		Status: AdminAgentStatusReady,
		Source: EditableAgentSource{Kind: "directory", Path: filepath.Join(existingDir, "agent.yml"), AgentDir: existingDir},
	}
	archive := buildAgentImportZIP(t, []agentArchiveTestEntry{
		{name: "agent.yml", content: "key: existing\nname: Replacement Agent\nmode: REACT\nmodelConfig:\n  modelKey: new-model\n"},
		{name: "new-only.txt", content: "new"},
	})

	_, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
	var conflict *AgentArchiveConflictError
	if !errors.As(err, &conflict) || conflict.Key != "existing" {
		t.Fatalf("expected existing-agent conflict, got %T %v", err, err)
	}
	assertAgentArchiveFileContent(t, filepath.Join(existingDir, "agent.yml"), oldConfig)

	mutation, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatalf("begin overwrite: %v", err)
	}
	if _, err := os.Stat(filepath.Join(existingDir, "new-only.txt")); err != nil {
		t.Fatalf("new resource missing before rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(existingDir, "old-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old resource should be replaced before rollback, got %v", err)
	}
	if err := registry.RollbackEditableAgentArchiveMutation(mutation); err != nil {
		t.Fatalf("rollback overwrite: %v", err)
	}
	assertAgentArchiveFileContent(t, filepath.Join(existingDir, "agent.yml"), oldConfig)
	if _, err := os.Stat(filepath.Join(existingDir, "old-only.txt")); err != nil {
		t.Fatalf("old resource missing after rollback: %v", err)
	}

	mutation, err = registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatalf("begin committed overwrite: %v", err)
	}
	if err := registry.CommitEditableAgentArchiveMutation(mutation); err != nil {
		t.Fatalf("commit overwrite: %v", err)
	}
	assertAgentArchiveFileContent(t, filepath.Join(existingDir, "new-only.txt"), "new")
	if _, err := os.Stat(filepath.Join(existingDir, "old-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old resource survived committed overwrite: %v", err)
	}
}

func TestEditableAgentArchiveOverwritesFlatAgentAsDirectory(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	flatPath := filepath.Join(agentsDir, "flat.yml")
	if err := os.WriteFile(flatPath, []byte("key: flat\nname: Flat\nmode: REACT\nmodelConfig:\n  modelKey: old\n"), 0o644); err != nil {
		t.Fatalf("write flat agent: %v", err)
	}
	registry := newAgentArchiveTestRegistry(agentsDir)
	registry.adminAgents["flat"] = AdminAgent{Key: "flat", Name: "Flat", Status: AdminAgentStatusReady, Source: EditableAgentSource{Kind: "file", Path: flatPath}}
	archive := buildAgentImportZIP(t, []agentArchiveTestEntry{{name: "agent.yml", content: "key: flat\nname: Flat Imported\nmode: REACT\nmodelConfig:\n  modelKey: new\n"}})

	mutation, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), true)
	if err != nil {
		t.Fatalf("begin flat overwrite: %v", err)
	}
	if err := registry.CommitEditableAgentArchiveMutation(mutation); err != nil {
		t.Fatalf("commit flat overwrite: %v", err)
	}
	if _, err := os.Stat(flatPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("flat source should be removed, got %v", err)
	}
	assertAgentArchiveFileContent(t, filepath.Join(agentsDir, "flat", "agent.yml"), "key: flat\nname: Flat Imported\nmode: REACT\nmodelConfig:\n  modelKey: new\n")
}

func TestEditableAgentArchiveRejectsUnsafeAndInvalidPackages(t *testing.T) {
	validConfig := "key: demo\nname: Demo\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"
	for _, tc := range []struct {
		name    string
		entries []agentArchiveTestEntry
		code    string
	}{
		{name: "path escape", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: "../escape.txt", content: "x"}}, code: "invalid_path"},
		{name: "backslash path", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: `assets\escape.txt`, content: "x"}}, code: "invalid_path"},
		{name: "ambiguous config", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: "agent.yaml", content: validConfig}}, code: "ambiguous_agent_config"},
		{name: "duplicate path", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: "docs/guide.md", content: "a"}, {name: "docs/guide.md", content: "b"}}, code: "duplicate_path"},
		{name: "case collision", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: "Docs/Guide.md", content: "a"}, {name: "docs/guide.md", content: "b"}}, code: "duplicate_path"},
		{name: "file directory conflict", entries: []agentArchiveTestEntry{{name: "agent.yml", content: validConfig}, {name: "assets", content: "file"}, {name: "assets/logo.txt", content: "child"}}, code: "path_conflict"},
		{name: "invalid definition root", entries: []agentArchiveTestEntry{{name: "agent.yml", content: "- not\n- a map\n"}}, code: "invalid_agent_definition"},
		{name: "invalid UTF-8", entries: []agentArchiveTestEntry{{name: "agent.yml", content: "key: demo\nname: \xff\nmode: REACT\n"}}, code: "invalid_agent_encoding"},
		{name: "invalid key", entries: []agentArchiveTestEntry{{name: "agent.yml", content: "key: ../demo\nname: Demo\nmode: REACT\n"}}, code: "invalid_agent_definition"},
		{name: "internal mode", entries: []agentArchiveTestEntry{{name: "agent.yml", content: "key: demo\nname: Demo\nmode: TEAM\n"}}, code: "invalid_agent_definition"},
		{name: "missing config", entries: []agentArchiveTestEntry{{name: "README.md", content: "missing"}}, code: "missing_agent_config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentsDir := filepath.Join(t.TempDir(), "agents")
			registry := newAgentArchiveTestRegistry(agentsDir)
			archive := buildAgentImportZIP(t, tc.entries)
			_, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
			var validation *AgentArchiveValidationError
			if !errors.As(err, &validation) || len(validation.Diagnostics) == 0 || validation.Diagnostics[0].Code != tc.code {
				t.Fatalf("expected validation code %q, got %T %v", tc.code, err, err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		agentsDir := filepath.Join(t.TempDir(), "agents")
		registry := newAgentArchiveTestRegistry(agentsDir)
		archive := buildAgentImportZIP(t, []agentArchiveTestEntry{
			{name: "agent.yml", content: validConfig},
			{name: "linked", content: "target", mode: os.ModeSymlink | 0o777},
		})
		_, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
		var validation *AgentArchiveValidationError
		if !errors.As(err, &validation) || validation.Diagnostics[0].Code != "symlink_not_allowed" {
			t.Fatalf("expected symlink rejection, got %T %v", err, err)
		}
	})

	t.Run("non-regular file", func(t *testing.T) {
		agentsDir := filepath.Join(t.TempDir(), "agents")
		registry := newAgentArchiveTestRegistry(agentsDir)
		archive := buildAgentImportZIP(t, []agentArchiveTestEntry{
			{name: "agent.yml", content: validConfig},
			{name: "named-pipe", mode: os.ModeNamedPipe | 0o644},
		})
		_, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
		var validation *AgentArchiveValidationError
		if !errors.As(err, &validation) || validation.Diagnostics[0].Code != "unsupported_entry" {
			t.Fatalf("expected non-regular entry rejection, got %T %v", err, err)
		}
	})

	t.Run("oversized agent YAML", func(t *testing.T) {
		agentsDir := filepath.Join(t.TempDir(), "agents")
		registry := newAgentArchiveTestRegistry(agentsDir)
		content := validConfig + "description: " + strings.Repeat("x", int(EditableAgentMaxSourceBytes))
		archive := buildAgentImportZIP(t, []agentArchiveTestEntry{{name: "agent.yml", content: content}})
		_, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), int64(len(archive)), false)
		var validation *AgentArchiveValidationError
		if !errors.As(err, &validation) || validation.Diagnostics[0].Code != "agent_config_too_large" {
			t.Fatalf("expected oversized config rejection, got %T %v", err, err)
		}
	})
}

func TestEditableAgentArchiveEnforcesUploadEntryAndExpandedSizeLimits(t *testing.T) {
	agentsDir := filepath.Join(t.TempDir(), "agents")
	registry := newAgentArchiveTestRegistry(agentsDir)
	archive := buildAgentImportZIP(t, []agentArchiveTestEntry{{name: "agent.yml", content: "key: limits\nname: Limits\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"}})
	if _, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(archive), EditableAgentMaxArchiveUploadBytes+1, false); !errors.Is(err, ErrAgentArchiveUploadTooLarge) {
		t.Fatalf("expected upload-size rejection, got %v", err)
	}

	tooManyEntries := make([]agentArchiveTestEntry, 0, EditableAgentMaxArchiveFiles+1)
	tooManyEntries = append(tooManyEntries, agentArchiveTestEntry{name: "agent.yml", content: "key: too-many\nname: Too Many\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"})
	for index := 0; index < EditableAgentMaxArchiveFiles; index++ {
		tooManyEntries = append(tooManyEntries, agentArchiveTestEntry{name: fmt.Sprintf("files/%04d.txt", index), content: "x"})
	}
	tooMany := buildAgentImportZIP(t, tooManyEntries)
	if _, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(tooMany), int64(len(tooMany)), false); !errors.Is(err, ErrAgentArchiveTooManyFiles) {
		t.Fatalf("expected entry-count rejection, got %v", err)
	}

	oversized := buildAgentImportZIP(t, []agentArchiveTestEntry{
		{name: "agent.yml", content: "key: oversized\nname: Oversized\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"},
		{name: "large.bin", content: "x"},
	})
	oversized = patchZIPDeclaredUncompressedSizes(t, oversized, map[string]uint32{"large.bin": uint32(EditableAgentMaxArchiveUploadBytes + 1)})
	if _, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(oversized), int64(len(oversized)), false); !errors.Is(err, ErrAgentArchiveTooLarge) {
		t.Fatalf("expected expanded-size rejection, got %v", err)
	}

	totalEntries := []agentArchiveTestEntry{{name: "agent.yml", content: "key: total-limit\nname: Total Limit\nmode: REACT\nmodelConfig:\n  modelKey: model-a\n"}}
	declaredSizes := map[string]uint32{}
	for index := 0; index < 9; index++ {
		name := fmt.Sprintf("files/large-%d.bin", index)
		totalEntries = append(totalEntries, agentArchiveTestEntry{name: name, content: "x"})
		declaredSizes[name] = uint32(EditableAgentMaxArchiveUploadBytes)
	}
	totalBomb := patchZIPDeclaredUncompressedSizes(t, buildAgentImportZIP(t, totalEntries), declaredSizes)
	if _, err := registry.BeginImportEditableAgentArchive(bytes.NewReader(totalBomb), int64(len(totalBomb)), false); !errors.Is(err, ErrAgentArchiveTooLarge) {
		t.Fatalf("expected total expanded-size rejection, got %v", err)
	}
}

func newAgentArchiveTestRegistry(agentsDir string) *FileRegistry {
	return &FileRegistry{
		cfg:         config.Config{Paths: config.PathsConfig{AgentsDir: agentsDir}},
		agents:      map[string]AgentDefinition{},
		adminAgents: map[string]AdminAgent{},
	}
}

func buildAgentImportZIP(t *testing.T, entries []agentArchiveTestEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		} else {
			header.SetMode(0o644)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %s: %v", entry.name, err)
		}
		if _, err := file.Write([]byte(entry.content)); err != nil {
			t.Fatalf("write ZIP entry %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close agent ZIP: %v", err)
	}
	return output.Bytes()
}

func assertAgentArchiveFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimSpace(string(content)) != strings.TrimSpace(expected) {
		t.Fatalf("%s content = %q, want %q", path, content, expected)
	}
}
