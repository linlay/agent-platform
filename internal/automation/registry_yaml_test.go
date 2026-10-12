package automation

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts/queryinput"
)

func TestRegistryPersistLoadPreservesQueryMessageExactly(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root)
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

func TestAutomationQueryRejectsUnknownFields(t *testing.T) {
	for _, value := range []any{nil, []any{}, []any{"resource"}} {
		_, err := parseQuery(map[string]any{"message": "hello", "unexpectedOption": value})
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("value=%#v error=%v", value, err)
		}
	}
}

func TestRegistryPersistOmitsEmptyAutomationDefaults(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root)
	definition := Definition{
		ID:       "minimal",
		Name:     "Minimal",
		Enabled:  true,
		Cron:     "0 9 * * *",
		AgentKey: "zenmi",
		Query:    Query{Message: "hello"},
	}

	if err := registry.Persist(definition); err != nil {
		t.Fatalf("persist automation: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, "minimal.yml"))
	if err != nil {
		t.Fatalf("read persisted automation: %v", err)
	}
	content := string(persisted)
	for _, field := range []string{"description:", "environment:", "  role:", "  hidden:"} {
		if strings.Contains(content, field) {
			t.Fatalf("expected default field %q to be omitted, got:\n%s", field, content)
		}
	}

	definitions, err := registry.Load()
	if err != nil || len(definitions) != 1 {
		t.Fatalf("reload persisted automation: definitions=%#v err=%v", definitions, err)
	}
	request := definitions[0].ToQueryRequest()
	if request.Role != api.QueryRoleAutomation || request.Hidden == nil || !*request.Hidden {
		t.Fatalf("unexpected execution defaults %#v", request)
	}
}

func TestRegistryPersistPreservesExplicitVisibleAutomationQuery(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry(root)
	hidden := false
	definition := Definition{
		ID:       "visible",
		Name:     "Visible",
		Enabled:  true,
		Cron:     "0 9 * * *",
		AgentKey: "zenmi",
		Query: Query{
			Role:    api.QueryRoleAutomation,
			Hidden:  &hidden,
			Message: "hello",
		},
	}

	if err := registry.Persist(definition); err != nil {
		t.Fatalf("persist automation: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, "visible.yml"))
	if err != nil {
		t.Fatalf("read persisted automation: %v", err)
	}
	content := string(persisted)
	if !strings.Contains(content, "  role: automation\n") || !strings.Contains(content, "  hidden: false\n") {
		t.Fatalf("expected explicit query overrides, got:\n%s", content)
	}

	definitions, err := registry.Load()
	if err != nil || len(definitions) != 1 {
		t.Fatalf("reload persisted automation: definitions=%#v err=%v", definitions, err)
	}
	request := definitions[0].ToQueryRequest()
	if request.Hidden == nil || *request.Hidden {
		t.Fatalf("expected explicit visible query, got %#v", request)
	}
}

func TestRegistryDoesNotDecodeLegacyPlainOrSingleQuotedMessageEscapes(t *testing.T) {
	registry := NewRegistry(t.TempDir())
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
	registry := NewRegistry(root)
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

func TestAutomationRequiredLibrariesPersistAndExecute(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	def := Definition{ID: "kb-run", Name: "KB Run", Enabled: true, Cron: "0 17 * * 5", AgentKey: "general", Query: automationQueryFromRequest(api.AutomationQueryRequest{Message: "retrieve", References: queryinput.MakeReferences("kbase", []string{" a ", "b", "a", " "})})}
	if err := registry.Persist(def); err != nil {
		t.Fatal(err)
	}
	definitions, err := registry.Load()
	if err != nil || len(definitions) != 1 {
		t.Fatalf("load: %+v %v", definitions, err)
	}
	got := definitions[0]
	if strings.Join(queryinput.ResourceIDs(got.Query.References, "kbase"), ",") != "a,b" || strings.Join(queryinput.ResourceIDs(got.ToQueryRequest().References, "kbase"), ",") != "a,b" || strings.Join(queryinput.ResourceIDs(mapAutomationQuery(got.Query).References, "kbase"), ",") != "a,b" {
		t.Fatalf("selection was lost: %+v", got.Query)
	}
	applyAutomationUpdate(&got, api.UpdateAutomationRequest{Query: &api.AutomationQueryRequest{Message: "clear", References: queryinput.MakeReferences("kbase", []string{})}})
	if len(queryinput.ResourceIDs(got.Query.References, "kbase")) != 0 {
		t.Fatal("empty update did not clear libraries")
	}
}
