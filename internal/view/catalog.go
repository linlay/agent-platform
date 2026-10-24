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
// one line per view: key and purpose. Schemas, queries and block syntax are
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
type Description struct {
	Key         string                      `json:"key"`
	Title       string                      `json:"title,omitempty"`
	Description string                      `json:"description"`
	DataSchema  json.RawMessage             `json:"dataSchema,omitempty"`
	Queries     map[string]QueryDescription `json:"queries,omitempty"`
	Usage       []string                    `json:"usage"`
}

// QueryDescription omits how a query runs; the model only writes params.
type QueryDescription struct {
	Description  string          `json:"description,omitempty"`
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
}

// Describe returns the display view named by key (<connectorId>/<viewKey>)
// among the given mounts, with block templates for static data and for each
// declared query.
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
		DataSchema:  compactJSON(d.DataSchema),
	}
	names := make([]string, 0, len(d.Queries))
	for name := range d.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		out.Queries = map[string]QueryDescription{}
	}
	for _, name := range names {
		q := d.Queries[name]
		out.Queries[name] = QueryDescription{
			Description:  strings.Join(strings.Fields(q.Description), " "),
			ParamsSchema: compactJSON(q.ParamsSchema),
		}
		// A query keeps the view current and lets the user change conditions,
		// so its template is listed before the static one.
		out.Usage = append(out.Usage, "```view key="+key+" query="+name+"\n{params object matching queries."+name+".paramsSchema}\n```")
	}
	out.Usage = append(out.Usage, "```view key="+key+"\n{data matching dataSchema}\n```")
	return out
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	var compact bytes.Buffer
	if len(raw) == 0 || json.Compact(&compact, raw) != nil {
		return nil
	}
	return compact.Bytes()
}
