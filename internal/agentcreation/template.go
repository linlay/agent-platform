// Package agentcreation expands the capability templates configured in
// configs/agent-creation.yml into the concrete tools, skills and connectors
// written to a new agent.yml. It is mode neutral: callers supply the lookups
// and the per-type creation tool lists.
package agentcreation

import (
	"fmt"
	"slices"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

const (
	TypeGeneral = "general"
	TypeCoder   = "coder"
	TypeKBase   = "kbase"
	// TypeACP is the external-engine card. It never takes capability groups:
	// an ACP bridge does not execute platform tools, skills or connectors.
	TypeACP = "acp"
)

// NativeTypes lists the creation types that accept capability groups.
func NativeTypes() []string {
	return []string{TypeGeneral, TypeCoder, TypeKBase}
}

const (
	CodeUnknownGroup      = "unknown_capability_group"
	CodeGroupUnavailable  = "capability_group_unavailable"
	CodeGroupConflict     = "capability_group_conflict"
	CodeGroupsUnsupported = "capability_groups_unsupported"
)

// Error is a creation-template rejection with a stable code for clients.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Lookup reports whether template members exist right now. Templates are read
// at startup while skills and connectors can change afterwards, so membership
// is always checked against the live catalog.
type Lookup struct {
	SkillExists     func(key string) bool
	ToolExists      func(name string) bool
	ConnectorExists func(id string) bool
	// ConnectorConflict returns a descriptive error when the connector ids
	// cannot be mounted on the same agent.
	ConnectorConflict func(ids []string) error
}

// Expansion is the merged, de-duplicated result of the selected groups.
type Expansion struct {
	Tools      []string
	Skills     []string
	Connectors []string
}

type Template struct {
	Config config.AgentCreationConfig
	// TypeTools is the built-in creation tool list per type, used when
	// agent-creation.yml does not set base-tools for that type.
	TypeTools map[string][]string
}

func (t Template) Group(key string) (config.AgentCreationGroupConfig, bool) {
	for _, group := range t.Config.Groups {
		if group.Key == key {
			return group, true
		}
	}
	return config.AgentCreationGroupConfig{}, false
}

// BaseTools returns the tools always written for a type.
func (t Template) BaseTools(typeKey string) []string {
	if item, ok := t.Config.Types[typeKey]; ok && item.BaseToolsSet {
		return append([]string(nil), item.BaseTools...)
	}
	return append([]string(nil), t.TypeTools[typeKey]...)
}

func (t Template) DefaultGroups(typeKey string) []string {
	return append([]string(nil), t.Config.Types[typeKey].DefaultGroups...)
}

// MissingMembers lists the members of a group that the live catalog does not
// have, as "skill x", "tool y" or "connector z".
func MissingMembers(group config.AgentCreationGroupConfig, lookup Lookup) []string {
	var missing []string
	check := func(kind string, names []string, exists func(string) bool) {
		if exists == nil {
			return
		}
		for _, name := range names {
			if !exists(name) {
				missing = append(missing, kind+" "+name)
			}
		}
	}
	check("skill", group.Skills, lookup.SkillExists)
	check("tool", group.Tools, lookup.ToolExists)
	check("connector", group.Connectors, lookup.ConnectorExists)
	return missing
}

// Expand merges the selected groups for one creation type. Members shared by
// several groups appear once, in first-selected order.
func (t Template) Expand(typeKey string, groupKeys []string, lookup Lookup) (Expansion, error) {
	var out Expansion
	selected := make([]string, 0, len(groupKeys))
	for _, key := range groupKeys {
		if key = strings.TrimSpace(key); key != "" && !slices.Contains(selected, key) {
			selected = append(selected, key)
		}
	}
	if typeKey == TypeACP {
		if len(selected) > 0 {
			return out, &Error{Code: CodeGroupsUnsupported, Message: "capability groups are not supported for engine: acp; the ACP bridge does not execute platform tools, skills or connectors"}
		}
		return out, nil
	}
	if !slices.Contains(NativeTypes(), typeKey) {
		return out, &Error{Code: CodeGroupsUnsupported, Message: fmt.Sprintf("capability groups are not supported for type %q", typeKey)}
	}
	for _, key := range selected {
		group, ok := t.Group(key)
		if !ok {
			return out, &Error{Code: CodeUnknownGroup, Message: fmt.Sprintf("capability group %q is not configured", key)}
		}
		if missing := MissingMembers(group, lookup); len(missing) > 0 {
			return out, &Error{Code: CodeGroupUnavailable, Message: fmt.Sprintf("capability group %q is unavailable: missing %s", key, strings.Join(missing, ", "))}
		}
		out.Tools = appendUnique(out.Tools, group.Tools...)
		out.Skills = appendUnique(out.Skills, group.Skills...)
		out.Connectors = appendUnique(out.Connectors, group.Connectors...)
	}
	if lookup.ConnectorConflict != nil && len(out.Connectors) > 1 {
		if err := lookup.ConnectorConflict(out.Connectors); err != nil {
			return out, &Error{Code: CodeGroupConflict, Message: "selected capability groups mount connectors that cannot be combined: " + err.Error()}
		}
	}
	return out, nil
}

// ApplyToDefinition writes an expansion into a creation definition. Lists the
// caller already put in the definition are kept and come first.
//
// modeDefaultTools is the tool set the type gets at load time when agent.yml
// declares none (CODER only). Writing any tool for such a type would replace
// that default, so the default is written out together with the additions;
// when there is nothing to add the list stays undeclared.
func ApplyToDefinition(definition map[string]any, baseTools []string, expansion Expansion, modeDefaultTools []string) map[string]any {
	out := contracts.CloneMap(definition)
	if out == nil {
		out = map[string]any{}
	}
	explicitTools := definitionNames(out, "toolConfig", "tools")
	additions := appendUnique(append([]string(nil), baseTools...), expansion.Tools...)
	switch {
	case len(explicitTools) == 0 && len(modeDefaultTools) > 0 && len(additions) == 0:
	case len(explicitTools) == 0 && len(modeDefaultTools) > 0:
		setDefinitionNames(out, "toolConfig", "tools", appendUnique(append([]string(nil), modeDefaultTools...), additions...))
	default:
		if tools := appendUnique(explicitTools, additions...); len(tools) > 0 {
			setDefinitionNames(out, "toolConfig", "tools", tools)
		}
	}
	if skills := appendUnique(definitionNames(out, "skillConfig", "skills"), expansion.Skills...); len(skills) > 0 {
		setDefinitionNames(out, "skillConfig", "skills", skills)
	}
	if connectors := appendUnique(definitionNames(out, "connectorConfig", "connectors"), expansion.Connectors...); len(connectors) > 0 {
		setDefinitionNames(out, "connectorConfig", "connectors", connectors)
	}
	return out
}

// LocalizedText picks the text for a locale, falling back to the language,
// then the untagged text, then any configured text.
func LocalizedText(texts map[string]string, locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if text := texts[locale]; text != "" {
		return text
	}
	if language, _, found := strings.Cut(locale, "-"); found {
		for key, text := range texts {
			if key == language || strings.HasPrefix(key, language+"-") {
				return text
			}
		}
	}
	if text := texts[""]; text != "" {
		return text
	}
	keys := make([]string, 0, len(texts))
	for key := range texts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if len(keys) > 0 {
		return texts[keys[0]]
	}
	return ""
}

func definitionNames(definition map[string]any, section string, field string) []string {
	node := contracts.AnyMapNode(definition[section])
	var out []string
	switch typed := node[field].(type) {
	case []string:
		out = appendUnique(out, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = appendUnique(out, text)
			}
		}
	}
	return out
}

func setDefinitionNames(definition map[string]any, section string, field string, names []string) {
	node := contracts.CloneMap(contracts.AnyMapNode(definition[section]))
	if node == nil {
		node = map[string]any{}
	}
	values := make([]any, 0, len(names))
	for _, name := range names {
		values = append(values, name)
	}
	node[field] = values
	definition[section] = node
}

func appendUnique(list []string, names ...string) []string {
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(list, name) {
			list = append(list, name)
		}
	}
	return list
}
