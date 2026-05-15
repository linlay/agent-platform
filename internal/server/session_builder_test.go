package server

import (
	"reflect"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/contracts"
)

func TestBuildSessionToolNamesDoesNotAutoAddInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime"}, true)
	want := []string{"datetime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesKeepsExplicitInvokeAgents(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime", contracts.InvokeAgentsToolName}, true)
	want := []string{"datetime", contracts.InvokeAgentsToolName}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestBuildSessionToolNamesFiltersInvokeAgentsWhenDisallowed(t *testing.T) {
	got := buildSessionToolNames([]string{"datetime", contracts.InvokeAgentsToolName}, false)
	want := []string{"datetime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSessionToolNames() = %#v, want %#v", got, want)
	}
}

func TestEffectiveAgentToolsForDesktopRequestAddsDesktopAction(t *testing.T) {
	got := effectiveAgentToolsForRequest(catalog.AgentDefinition{
		Tools: []string{"datetime"},
	}, api.QueryRequest{
		Params: map[string]any{
			"desktop": map[string]any{"source": "copilot"},
		},
	})
	want := []string{"datetime", "desktop_action"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effectiveAgentToolsForRequest() = %#v, want %#v", got, want)
	}
}

func TestEffectiveAgentToolsForDesktopRequestKeepsExplicitDesktopAction(t *testing.T) {
	got := effectiveAgentToolsForRequest(catalog.AgentDefinition{
		Tools: []string{"datetime", "desktop_action"},
	}, api.QueryRequest{
		Params: map[string]any{
			"desktop": true,
		},
	})
	want := []string{"datetime", "desktop_action"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effectiveAgentToolsForRequest() = %#v, want %#v", got, want)
	}
}
