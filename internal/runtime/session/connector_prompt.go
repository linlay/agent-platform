package session

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
)

const maxConnectorCatalogBytes = 32 << 10

// BuildConnectorCatalogPrompt reads only the executor's frozen mounts. Package
// metadata describes purpose, not remote readiness or additional permissions.
func BuildConnectorCatalogPrompt(def catalog.AgentDefinition, locale string, config contracts.ConnectorAppendConfig) (string, error) {
	instructions := strings.TrimSpace(config.InstructionsPrompt)
	header := strings.TrimSpace(config.CatalogHeader)
	if instructions == "" || header == "" || def.Engine == catalog.AgentEngineACP || IsProxyRoutedAgent(def) {
		return "", nil
	}
	entries := make([]string, 0, len(def.ConnectorMounts))
	for _, mount := range def.ConnectorMounts {
		pkg, err := connector.LoadDirectory(mount.Dir, mount.ID)
		if err != nil {
			return "", fmt.Errorf("connector catalog %s: %w", mount.ID, err)
		}
		manifest := pkg.Manifest.Localized(locale)
		entry := "- " + manifest.ID + " | " + strings.Join(strings.Fields(manifest.Name), " ")
		if description := strings.Join(strings.Fields(manifest.Description), " "); description != "" {
			entry += " | " + description
		}
		entries = append(entries, entry)
	}
	sort.Strings(entries)
	size, kept := 0, 0
	for _, entry := range entries {
		next := size + len(entry)
		if kept > 0 {
			next++
		}
		if next > maxConnectorCatalogBytes {
			break
		}
		size, kept = next, kept+1
	}
	if omitted := len(entries) - kept; omitted > 0 {
		log.Printf("[runtime][connector-catalog][warn] agent %s: %d connectors omitted, catalog exceeds %d bytes", def.Key, omitted, maxConnectorCatalogBytes)
	}
	if kept == 0 {
		return "", nil
	}
	return instructions + "\n\n" + header + "\n" + strings.Join(entries[:kept], "\n"), nil
}
