// Package connectorops executes installed connectors without an Agent or model.
package connectorops

import (
	"agent-platform/internal/connector"
	"sort"
)

const MaxJSONBytes = 1 << 20

// Permissions grant full connector execution, not an inferred read-only subset.
type Permission struct {
	ConnectorID string `json:"connectorId"`
	Adapter     string `json:"adapter"`
}
type Catalog struct {
	ConnectorID string   `json:"connectorId"`
	Revision    string   `json:"revision"`
	Adapters    []string `json:"adapters"`
	Components  []string `json:"components"`
}

func Load(pkg connector.Package) (Catalog, error) {
	revision, err := connector.RuntimeFingerprint(pkg.Dir)
	c := Catalog{ConnectorID: pkg.ID, Revision: revision, Adapters: []string{}, Components: []string{}}
	if pkg.CLI != nil {
		c.Adapters = append(c.Adapters, "cli")
	}
	if len(pkg.MCP) > 0 {
		c.Adapters = append(c.Adapters, "mcp")
		for name := range pkg.MCP {
			c.Components = append(c.Components, name)
		}
		sort.Strings(c.Components)
	}
	return c, err
}
