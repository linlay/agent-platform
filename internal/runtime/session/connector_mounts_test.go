package session

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"reflect"
	"testing"
)

func TestNativeConnectorToolsFollowFrozenMount(t *testing.T) {
	tools := []string{"desktop_shell", "workpanel_open", "surface_cdp", "awcp_invoke"}
	for id, want := range map[string]map[string]string{
		connector.PlatformControlConnectorID: {"desktop_shell": connector.PlatformControlConnectorID},
		connector.WebControlConnectorID:      {"workpanel_open": connector.WebControlConnectorID, "surface_cdp": connector.WebControlConnectorID, "awcp_invoke": connector.WebControlConnectorID},
	} {
		// A tool is granted only by the mounted connector that owns it.
		def := catalog.AgentDefinition{ConnectorNativeTools: tools, ConnectorMounts: []catalog.ConnectorMount{{ID: id, Dir: "/frozen/package"}}}
		if got := RuntimeNativeConnectorTools(def); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v", id, got)
		}
		def.ConnectorMounts = nil
		if got := RuntimeNativeConnectorTools(def); len(got) != 0 {
			t.Fatalf("missing mount grants tools: %v", got)
		}
	}
}

func TestRuntimeModeToolNamesHidesDesktopOnlyPageTools(t *testing.T) {
	tools := []string{"datetime", "desktop_shell", "workpanel_state", "workpanel_open", "workpanel_close", "surface_list", "surface_cdp", "awcp_manual", "awcp_invoke"}
	if got := RuntimeModeToolNames(tools, config.RuntimeModeDesktop); !reflect.DeepEqual(got, tools) {
		t.Fatalf("desktop runtime: %v", got)
	}
	// A standalone WebClient hosts the WorkPanel but no controllable pages.
	want := []string{"datetime", "workpanel_state", "workpanel_open", "workpanel_close"}
	if got := RuntimeModeToolNames(tools, config.RuntimeModeStandalone); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone runtime: %v", got)
	}
}
