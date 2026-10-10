package catalog

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeSkillPathConflictPreservesPublishedAgent(t *testing.T) {
	for _, standalone := range []string{"suite", "SUITE"} {
		for _, memberFirst := range []bool{false, true} {
			keys := []string{standalone, "suite/demo"}
			if memberFirst {
				keys[0], keys[1] = keys[1], keys[0]
			}
			t.Run(strings.Join(keys, ","), func(t *testing.T) {
				root := t.TempDir()
				agentsDir, center := filepath.Join(root, "agents"), filepath.Join(root, "skills-center")
				private := filepath.Join(agentsDir, "writer", "skills", standalone)
				writeRuntimeAssemblerSkill(t, private, "Private suite")
				writeRuntimeAssemblerSkill(t, filepath.Join(private, "demo"), "Private nested resource")
				writeRuntimeAssemblerFile(t, filepath.Join(private, "demo", "README.md"), "private resource")
				writeRuntimeAssemblerFile(t, filepath.Join(center, "suite", "package.json"), `{"name":"suite","skills":[{"key":"demo"},{"key":"other"}]}`)
				writeRuntimeAssemblerSkill(t, filepath.Join(center, "suite", "demo"), "Shared demo")
				writeRuntimeAssemblerFile(t, filepath.Join(center, "suite", "demo", "README.md"), "shared resource")
				writeRuntimeAssemblerAgent(t, agentsDir, "writer", []string{standalone})
				assembler, err := newVersionTestAssembler(t, filepath.Join(root, "ru-agents"), center)
				if err != nil {
					t.Fatal(err)
				}
				loaded, admin, err := loadAgentsWithAdminAssembler(agentsDir, filepath.Join(root, "chats"), true, assembler)
				if err != nil {
					t.Fatal(err)
				}
				previous, ok := loaded["writer"]
				if !ok {
					t.Fatalf("initial Agent invalid: %+v", admin["writer"])
				}
				before := runtimeSkillPathTestFiles(t, previous.RuntimeDir)
				writeRuntimeAssemblerSkill(t, private, "Changed private suite")
				writeRuntimeAssemblerAgent(t, agentsDir, "writer", keys)
				loaded, admin, err = loadAgentsWithAdminAssembler(agentsDir, filepath.Join(root, "chats"), true, assembler)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := loaded["writer"]; ok {
					t.Fatal("Agent with overlapping skill destinations was accepted")
				}
				diagnostic := firstRuntimeAssemblerDiagnostic(admin["writer"])
				if diagnostic.Code != "runtime_skill_path_conflict" ||
					!strings.Contains(diagnostic.Message, standalone) || !strings.Contains(diagnostic.Message, "suite/demo") {
					t.Fatalf("missing actionable path-conflict diagnostic: %+v", diagnostic)
				}
				if after := runtimeSkillPathTestFiles(t, previous.RuntimeDir); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected configuration changed published runtime: before=%v after=%v", before, after)
				}
				entries, err := os.ReadDir(filepath.Join(assembler.root, ".staging"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejected candidate retained staging files: %v %v", entries, err)
				}
			})
		}
	}
}

func TestRuntimeSkillPathsAllowStandaloneAndPackageMembers(t *testing.T) {
	for _, keys := range [][]string{
		{"demo", "suite/demo", "suite/other", "suite-extra"},
		{"suite/other", "suite/demo", "demo", "suite-extra"},
	} {
		t.Run(strings.Join(keys, ","), func(t *testing.T) {
			root := t.TempDir()
			agentsDir, center := filepath.Join(root, "agents"), filepath.Join(root, "skills-center")
			writeRuntimeAssemblerFile(t, filepath.Join(center, "suite", "package.json"), `{"name":"suite","skills":[{"key":"demo"},{"key":"other"}]}`)
			for _, key := range keys {
				writeRuntimeAssemblerSkill(t, filepath.Join(center, filepath.FromSlash(key)), key)
			}
			writeRuntimeAssemblerAgent(t, agentsDir, "writer", keys)
			assembler, err := newVersionTestAssembler(t, filepath.Join(root, "ru-agents"), center)
			if err != nil {
				t.Fatal(err)
			}
			loaded, admin, err := loadAgentsWithAdminAssembler(agentsDir, filepath.Join(root, "chats"), true, assembler)
			if err != nil {
				t.Fatal(err)
			}
			def, ok := loaded["writer"]
			if !ok {
				t.Fatalf("non-overlapping skills rejected: %+v", admin["writer"])
			}
			for _, key := range keys {
				assertRuntimeAssemblerContent(t, runtimeSkillTestPath(t, def.RuntimeDir, key, "SKILL.md"), "# "+key+"\n\nInstructions")
			}
		})
	}
}

func runtimeSkillPathTestFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		files[rel] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
