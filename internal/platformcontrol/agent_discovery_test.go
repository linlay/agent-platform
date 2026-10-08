package platformcontrol

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type agentDiscoveryRegistry struct {
	catalog.Registry
	items       []catalog.AdminAgent
	definitions map[string]catalog.AgentDefinition
}

func (r agentDiscoveryRegistry) AdminAgents() []catalog.AdminAgent { return r.items }
func (r agentDiscoveryRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	d, ok := r.definitions[key]
	return d, ok
}

func TestAgentDiscoverySummariesEligibilityAndPagination(t *testing.T) {
	r := agentDiscoveryRegistry{definitions: map[string]catalog.AgentDefinition{}}
	for i := 0; i < 105; i++ {
		key := fmt.Sprintf("a%03d", i)
		r.items = append(r.items, catalog.AdminAgent{Key: key, Name: "Name " + key, Role: "developer", Description: "Summary", Mode: "GENERAL"})
		r.definitions[key] = catalog.AgentDefinition{Key: key, Mode: "GENERAL", VisibilityScopes: []string{"invoke"}}
	}
	// The current caller remains visible. Warning diagnostics do not make it invalid.
	r.items = append(r.items, catalog.AdminAgent{Key: "caller", Name: "Current", Mode: "GENERAL", Diagnostics: []catalog.AdminAgentDiagnostic{{Severity: "warning", Code: "context_agents_ignored"}}})
	r.definitions["caller"] = catalog.AgentDefinition{Key: "caller", Mode: "GENERAL"}
	r.items = append(r.items, catalog.AdminAgent{Key: "invalid", Name: "Broken", Role: "worker", Description: "Broken summary", Mode: "GENERAL"})
	r.items = append(r.items, catalog.AdminAgent{Key: "hidden", Mode: "TEAM"})
	r.definitions["hidden"] = catalog.AgentDefinition{Key: "hidden", Mode: "TEAM"}
	d := r.definitions["a001"]
	d.Tools = []string{"agent_invoke"}
	r.definitions["a001"] = d
	d = r.definitions["a002"]
	d.Mode = "CHANNEL"
	r.definitions["a002"] = d
	h := NewToolHandler(config.Config{}, r, nil)
	first := discoveryQuery(t, h, "list", map[string]any{"resourceType": "agent", "limit": float64(100)})
	items := discoveredAgents(first)
	if len(items) != 100 || first["total"] != float64(107) || first["hasMore"] != true {
		t.Fatalf("page=%#v", first)
	}
	if items[0]["key"] != "a000" || items[0]["name"] != "Name a000" || items[0]["role"] != "developer" || items[0]["description"] != "Summary" || items[0]["mode"] != "GENERAL" || items[0]["invocable"] != true {
		t.Fatalf("summary=%#v", items[0])
	}
	if items[1]["invocable"] != false || items[2]["invocable"] != false {
		t.Fatal("ineligible targets advertised as invocable")
	}
	second := discoveryQuery(t, h, "list", map[string]any{"resourceType": "agent", "cursor": first["nextCursor"], "limit": float64(100)})
	rest := discoveredAgents(second)
	if len(rest) != 7 || second["hasMore"] != false {
		t.Fatalf("last page=%#v", second)
	}
	if rest[5]["key"] != "caller" || rest[5]["valid"] != true || rest[5]["invocable"] != false {
		t.Fatalf("caller=%#v", rest[5])
	}
	if rest[6]["key"] != "invalid" || rest[6]["name"] != "Broken" || rest[6]["valid"] != false || rest[6]["invocable"] != false {
		t.Fatalf("invalid=%#v", rest[6])
	}
	valid := discoveryQuery(t, h, "list", map[string]any{"resourceType": "agent", "status": "valid"})
	if valid["total"] != float64(106) {
		t.Fatalf("valid=%#v", valid)
	}
	internalQuery := func(p map[string]any) map[string]any {
		result, err := h.catalogQuery(context.Background(), "list", p)
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}
	for _, limit := range []float64{101, 1e100} {
		page := internalQuery(map[string]any{"resourceType": "agent", "limit": limit})
		if len(discoveredAgents(page)) != 100 {
			t.Fatalf("large limit %v not clamped", limit)
		}
	}
	for _, limit := range []float64{-1e100, -1, 0, 0.5, 1} {
		page := internalQuery(map[string]any{"resourceType": "agent", "limit": limit})
		if len(discoveredAgents(page)) != 1 {
			t.Fatalf("limit %v: %#v", limit, page)
		}
		end := internalQuery(map[string]any{"resourceType": "agent", "limit": limit, "cursor": "zzzz"})
		if len(discoveredAgents(end)) != 0 || end["hasMore"] != false {
			t.Fatalf("end=%#v", end)
		}
	}
	b, _ := json.Marshal(first)
	for _, field := range []string{"workspaceRoot", "runtimeConfig", "modelKey", "systemPrompt"} {
		if strings.Contains(string(b), field) {
			t.Fatalf("configuration leaked: %s", b)
		}
	}
}

func TestAgentDiscoveryWarningRemainsValid(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents")}}
	discoveryWrite(t, filepath.Join(cfg.Paths.AgentsDir, "caller", "agent.yml"), "key: caller\nmode: GENERAL\nmodelConfig: {modelKey: test}\ncontextConfig:\n  tags:\n    - agents\n  agents:\n    invalid: type\n")
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(cfg, registry, nil)
	page := discoveryQuery(t, h, "list", map[string]any{"resourceType": "agent", "status": "valid"})
	items := discoveredAgents(page)
	if len(items) != 1 || items[0]["valid"] != true {
		t.Fatalf("warning excluded valid Agent: %#v", page)
	}
	warnings := items[0]["diagnostics"].([]any)
	if len(warnings) != 1 || warnings[0].(map[string]any)["Severity"] != "warning" {
		t.Fatalf("warnings=%#v", warnings)
	}
}

func discoveredAgents(page map[string]any) []map[string]any {
	if items, ok := page["items"].([]map[string]any); ok {
		return items
	}
	var items []map[string]any
	for _, item := range page["items"].([]any) {
		items = append(items, item.(map[string]any))
	}
	return items
}
