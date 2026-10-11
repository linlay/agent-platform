package automation

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestAutomationAccessLevelPersistReloadAndDispatch(t *testing.T) {
	for _, level := range []string{"", "default", "auto_approve", "full_access"} {
		t.Run("level="+level, func(t *testing.T) {
			registry := NewRegistry(t.TempDir())
			def := Definition{ID: "daily", Name: "Daily", Enabled: true, Cron: "0 9 * * *", AgentKey: "agent", Query: Query{Message: "run", AccessLevel: level}}
			if err := registry.Persist(def); err != nil {
				t.Fatal(err)
			}
			defs, err := registry.Load()
			if err != nil || len(defs) != 1 {
				t.Fatalf("load: %#v %v", defs, err)
			}
			if defs[0].Query.AccessLevel != level {
				t.Fatalf("permission lost: %#v", defs[0].Query)
			}
			source, err := registry.ReadEditableSource("daily")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(source.Content, "accessLevel:") != (level != "") {
				t.Fatalf("unexpected source: %s", source.Content)
			}
			expected, _ := contracts.NormalizeAccessLevel(level)
			var called bool
			dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
				called = true
				actual, valid := contracts.NormalizeAccessLevel(req.AccessLevel)
				if !valid || actual != expected {
					t.Fatalf("want %s got %q", expected, req.AccessLevel)
				}
				return successfulTestQuery(req, hooks), nil
			}, nil, nil)
			if err := dispatcher.Dispatch(context.Background(), defs[0], "UTC"); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("query not dispatched")
			}
		})
	}
}

func TestAutomationAccessLevelValidationAndSourceEditing(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	def := Definition{ID: "daily", Name: "Daily", Enabled: true, Cron: "0 9 * * *", AgentKey: "agent", Query: Query{Message: "run", AccessLevel: "full_access"}}
	if err := registry.Persist(def); err != nil {
		t.Fatal(err)
	}
	source, err := registry.ReadEditableSource("daily")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"unsafe", "true", "42", "[default]"} {
		bad := strings.Replace(source.Content, "accessLevel: full_access", "accessLevel: "+value, 1)
		if _, err := registry.WriteEditableSource("daily", bad, source.SHA256); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	def.Query.AccessLevel = "unsafe"
	if err := registry.Persist(def); err == nil {
		t.Fatal("accepted invalid API definition")
	}
	changed := strings.Replace(source.Content, "accessLevel: full_access", "accessLevel: auto_approve", 1)
	if _, err := registry.WriteEditableSource("daily", changed, source.SHA256); err != nil {
		t.Fatal(err)
	}
	defs, err := registry.Load()
	if err != nil || len(defs) != 1 || defs[0].Query.AccessLevel != "auto_approve" {
		t.Fatalf("source edit lost: %#v %v", defs, err)
	}
}
