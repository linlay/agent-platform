package view

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// MaxCatalogBytes bounds the rendered prompt catalog of one Run. Entries past
// the limit are dropped whole, never truncated mid-entry.
const MaxCatalogBytes = 32 << 10

// RenderCatalog lists the display views of the given mounts for the model.
// Field labels are structure, not wording; all instructions and the header
// come from agent-prompt.yml. It returns the entries and how many were omitted.
func RenderCatalog(mounts []Mount) (string, int) {
	var entries []string
	for _, mount := range mounts {
		for key, d := range mount.Views {
			if d.Supports("display") {
				entries = append(entries, catalogEntry(mount.ID, key, d))
			}
		}
	}
	// Keys are unique per mount and mounts per Agent, so sorting the rendered
	// entries (which start with key: <connectorId>/<viewKey>) is deterministic.
	sort.Strings(entries)
	size, kept := 0, 0
	for _, entry := range entries {
		next := size + len(entry)
		if kept > 0 {
			next += 2
		}
		if next > MaxCatalogBytes {
			break
		}
		size, kept = next, kept+1
	}
	return strings.Join(entries[:kept], "\n\n"), len(entries) - kept
}

func catalogEntry(connectorID, key string, d Definition) string {
	lines := []string{"key: " + connectorID + "/" + key}
	if title := strings.TrimSpace(d.Title); title != "" {
		lines = append(lines, "title: "+title)
	}
	lines = append(lines, "description: "+strings.Join(strings.Fields(d.Description), " "))
	if len(d.DataSchema) > 0 {
		var compact bytes.Buffer
		if json.Compact(&compact, d.DataSchema) == nil {
			lines = append(lines, "data: "+compact.String())
		}
	}
	return strings.Join(lines, "\n")
}
