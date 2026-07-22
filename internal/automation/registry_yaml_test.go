package automation

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRegistryPersistLoadPreservesQueryMessageExactly(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root, nil)
	message := "  第一行\n第二行\t\\n 'single' \"double\" # hash\r\n结尾  "
	definition := Definition{
		ID:          "escaped-message",
		Name:        "Escaped Message",
		Description: "Preserve query message",
		Enabled:     true,
		Cron:        "0 17 * * 5",
		AgentKey:    "zenmi",
		Query:       Query{Message: message},
	}

	if err := registry.Persist(definition); err != nil {
		t.Fatalf("persist automation: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, "escaped-message.yml"))
	if err != nil {
		t.Fatalf("read persisted automation: %v", err)
	}
	wantLine := "  message: " + strconv.Quote(message) + "\n"
	if !strings.Contains(string(persisted), wantLine) {
		t.Fatalf("expected canonical reversible message scalar %q, got:\n%s", wantLine, persisted)
	}

	definitions, err := registry.Load()
	if err != nil {
		t.Fatalf("load automations: %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("expected one automation, got %d", len(definitions))
	}
	if got := definitions[0].Query.Message; got != message {
		t.Fatalf("message changed after persistence, want %q got %q", message, got)
	}
	if got := definitions[0].ToQueryRequest().Message; got != message {
		t.Fatalf("execution query message changed, want %q got %q", message, got)
	}
}

func TestRegistryDoesNotDecodeLegacyPlainOrSingleQuotedMessageEscapes(t *testing.T) {
	registry := NewRegistry(t.TempDir(), nil)
	want := `first\nsecond`
	for _, test := range []struct {
		name        string
		messageYAML string
	}{
		{name: "plain", messageYAML: want},
		{name: "single-quoted", messageYAML: "'" + want + "'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := []byte("name: Legacy\n" +
				"description: Legacy automation\n" +
				"enabled: true\n" +
				"cron: '0 17 * * 5'\n" +
				"agentKey: zenmi\n" +
				"query:\n" +
				"  message: " + test.messageYAML + "\n")
			definition, err := registry.parseDefinitionBytes(test.name, content)
			if err != nil {
				t.Fatalf("parse legacy automation: %v", err)
			}
			if got := definition.Query.Message; got != want {
				t.Fatalf("legacy message changed, want %q got %q", want, got)
			}
		})
	}
}

func TestRegistryPreservesEnvironmentExpressionQueryMessage(t *testing.T) {
	t.Setenv("AUTOMATION_MESSAGE_LITERAL", "expanded")
	root := t.TempDir()
	registry := NewRegistry(root, nil)
	message := "${AUTOMATION_MESSAGE_LITERAL}"
	definition := Definition{
		ID:          "environment-expression",
		Name:        "Environment Expression",
		Description: "Preserve query message",
		Enabled:     true,
		Cron:        "0 17 * * 5",
		AgentKey:    "zenmi",
		Query:       Query{Message: message},
	}

	if err := registry.Persist(definition); err != nil {
		t.Fatalf("persist automation: %v", err)
	}
	definitions, err := registry.Load()
	if err != nil {
		t.Fatalf("load automations: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Query.Message != message {
		t.Fatalf("environment expression message changed, want %q got %#v", message, definitions)
	}
}
