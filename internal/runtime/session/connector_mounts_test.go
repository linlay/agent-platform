package session

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"reflect"
	"testing"
)

func TestNativeConnectorToolsFollowFrozenMount(t *testing.T) {
	for _, id := range []string{connector.DesktopConnectorID, connector.DesktopWebConnectorID} {
		def := catalog.AgentDefinition{ConnectorNativeTools: []string{"desktop_action", "desktop_cdp"}, ConnectorMounts: []catalog.ConnectorMount{{ID: id, Dir: "/frozen/package"}}}
		want := map[string]string{"desktop_action": id, "desktop_cdp": id}
		if got := RuntimeNativeConnectorTools(def); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v", id, got)
		}
		def.ConnectorMounts = nil
		if got := RuntimeNativeConnectorTools(def); len(got) != 0 {
			t.Fatalf("missing mount grants tools: %v", got)
		}
	}
}
