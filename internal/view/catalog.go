package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MaxCatalogBytes bounds the rendered prompt catalog of one Run. Entries past
// the limit are dropped whole, never truncated mid-entry.
const MaxCatalogBytes = 32 << 10

// RenderCatalog lists the display views of the given mounts for the model,
// one line per view: key and purpose. The input schema and block syntax are
// disclosed on demand by Describe, so the catalog stays small as packages add
// views. All instructions and the header come from agent-prompt.yml. It
// returns the entries and how many were omitted.
func RenderCatalog(mounts []Mount) (string, int) {
	var entries []string
	for _, mount := range mounts {
		for key, d := range mount.Views {
			if d.Supports("display") {
				entries = append(entries, "- "+mount.ID+"/"+key+" — "+strings.Join(strings.Fields(d.Description), " "))
			}
		}
	}
	// Keys are unique per mount and mounts per Agent, so sorting the rendered
	// entries (which start with <connectorId>/<viewKey>) is deterministic.
	sort.Strings(entries)
	size, kept := 0, 0
	for _, entry := range entries {
		next := size + len(entry)
		if kept > 0 {
			next++
		}
		if next > MaxCatalogBytes {
			break
		}
		size, kept = next, kept+1
	}
	return strings.Join(entries[:kept], "\n"), len(entries) - kept
}

// Description is everything the model needs to write one display view block.
// Declared queries are the template's own business and stay undisclosed.
type Description struct {
	Key         string                      `json:"key"`
	Title       string                      `json:"title,omitempty"`
	Description string                      `json:"description"`
	InputSchema json.RawMessage             `json:"inputSchema,omitempty"`
	Actions     map[string]QueryDescription `json:"actions,omitempty"`
	Usage       []string                    `json:"usage"`
}

// QueryDescription omits how an action runs; the model only reads its params.
type QueryDescription struct {
	Description  string          `json:"description,omitempty"`
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
}

// Describe returns the display view named by key (<connectorId>/<viewKey>)
// among the given mounts, with its block template.
func Describe(mounts []Mount, key string) (Description, error) {
	connectorID, viewKey, ok := strings.Cut(strings.TrimSpace(key), "/")
	if ok {
		for _, mount := range mounts {
			if mount.ID != connectorID {
				continue
			}
			if d, found := mount.Views[viewKey]; found && d.Supports("display") {
				return describe(connectorID+"/"+viewKey, d), nil
			}
		}
	}
	return Description{}, fmt.Errorf("%w: no display view %q", ErrNotFound, key)
}

func describe(key string, d Definition) Description {
	out := Description{
		Key:         key,
		Title:       strings.TrimSpace(d.Title),
		Description: strings.Join(strings.Fields(d.Description), " "),
		InputSchema: compactJSON(d.InputSchema),
		Usage:       []string{"```view key=" + key + "\n{input matching inputSchema}\n```"},
	}
	if len(d.Actions) > 0 {
		out.Actions = make(map[string]QueryDescription)
		for name, action := range d.Actions {
			out.Actions[name] = QueryDescription{Description: strings.Join(strings.Fields(action.Description), " "), ParamsSchema: compactJSON(action.ParamsSchema)}
		}
	}
	return out
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	var compact bytes.Buffer
	if len(raw) == 0 || json.Compact(&compact, raw) != nil {
		return nil
	}
	return compact.Bytes()
}
