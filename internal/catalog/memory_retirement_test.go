package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRejectsRetiredMemoryTools(t *testing.T) {
	for _, name := range []string{"memory_read", "memory_search", "memory_write", "memory_update"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yml")
			content := "key: demo\nmode: GENERAL\nmodelConfig:\n  modelKey: demo-model\nmemoryConfig:\n  enabled: true\ntoolConfig:\n  tools:\n    - " + name + "\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := parseAgentDefinitionForTest(path)
			if err == nil || !strings.Contains(err.Error(), name+" is retired") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRetiredMemoryToolsCannotBePresetsOrManagedDeclarations(t *testing.T) {
	for _, name := range []string{"memory_read", "memory_search", "memory_write", "memory_update"} {
		if err := ValidateOrdinaryAgentTools([]string{name}); err == nil {
			t.Fatalf("managed declaration accepted %s", name)
		}
		if err := validatePresetTools([]string{name}, nil); err == nil {
			t.Fatalf("unregistered preset accepted %s", name)
		}
	}
}
