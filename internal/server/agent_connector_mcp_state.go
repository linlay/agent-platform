package server

import (
	"slices"
	"sort"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/mcp"
)

// This is a projection of cached discovery state. Reading it cannot initialize
// a server, execute a CLI or use a package retained only by an older Run.
func (s *Server) agentConnectorMCPState(def catalog.AgentDefinition) []api.AgentConnectorMCPStatus {
	result := []api.AgentConnectorMCPStatus{}
	for _, mount := range def.ConnectorMounts {
		pkg, err := connector.LoadDirectory(mount.Dir, mount.ID)
		if err != nil {
			result = append(result, api.AgentConnectorMCPStatus{
				ConnectorID: mount.ID, ServerKey: mount.ID, Status: "unavailable",
			})
			continue
		}
		for _, sourceKey := range mcp.EnabledConnectorServerKeys(pkg) {
			key := connector.AgentVersionServerKey(def.Key, sourceKey, mount.Digest)
			if !slices.Contains(def.ConnectorMCPServers, key) {
				continue
			}
			entry := api.AgentConnectorMCPStatus{
				ConnectorID: mount.ID, ServerKey: sourceKey, Status: "pending",
			}
			if s.deps.MCPToolSyncStatus != nil {
				if current, ok := s.deps.MCPToolSyncStatus.ServerStatus(key); ok {
					switch current.Status {
					case "pending", "syncing", "ready", "unavailable":
						entry.Status = current.Status
					default:
						entry.Status = "unavailable"
					}
				}
			}
			if entry.Status == "ready" {
				entry.ToolCount = s.connectorMCPToolCount(key)
			}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ConnectorID == result[j].ConnectorID {
			return result[i].ServerKey < result[j].ServerKey
		}
		return result[i].ConnectorID < result[j].ConnectorID
	})
	return result
}
