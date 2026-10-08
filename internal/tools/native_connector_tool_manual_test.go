package tools

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/resources"
)

type nativeToolManualChapter struct {
	file   string
	marker string
}

// A shared action/args manual is checked as one business chapter. Tools with
// top-level arguments are checked against their own heading or list item, so
// an argument documented for another tool does not count as coverage.
var nativeToolManualChapters = map[string]nativeToolManualChapter{
	"desktop_kanban":     {file: "references/kanban.md"},
	"catalog_query":      {file: "references/catalog.md"},
	"catalog_manage":     {file: "references/catalog.md"},
	"platform_inspect":   {file: "SKILL.md"},
	"desktop_shell":      {file: "references/shell.md"},
	"desktop_settings":   {file: "references/settings.md"},
	"desktop_site":       {file: "references/website.md"},
	"desktop_webapp":     {file: "references/webapp.md"},
	"desktop_service":    {file: "references/control-center.md"},
	"desktop_market":     {file: "references/market.md"},
	"chat_start":         {file: "references/run.md", marker: "## chat_start"},
	"chat_get_status":    {file: "references/run.md", marker: "## chat_get_status"},
	"chat_interrupt":     {file: "references/run.md", marker: "## chat_interrupt"},
	"chat_query":         {file: "references/chat.md"},
	"chat_manage":        {file: "references/chat.md"},
	"automation_query":   {file: "references/automation.md"},
	"automation_manage":  {file: "references/automation.md"},
	"workpanel_state":    {file: "references/workpanel.md", marker: "- `workpanel_state`:"},
	"workpanel_open":     {file: "references/workpanel.md", marker: "- `workpanel_open`:"},
	"workpanel_close":    {file: "references/workpanel.md", marker: "- `workpanel_close`:"},
	"surface_list":       {file: "references/surface.md", marker: "- `surface_list`:"},
	"surface_state":      {file: "references/surface.md", marker: "- `surface_state`:"},
	"surface_navigate":   {file: "references/surface.md", marker: "- `surface_navigate`:"},
	"surface_activate":   {file: "references/surface.md", marker: "- `surface_activate`:"},
	"surface_close":      {file: "references/surface.md", marker: "- `surface_close`:"},
	"surface_screenshot": {file: "references/surface.md", marker: "- `surface_screenshot`:"},
	"surface_evaluate":   {file: "references/surface.md", marker: "- `surface_evaluate`:"},
	"surface_click":      {file: "references/surface.md", marker: "- `surface_click`:"},
	"surface_element":    {file: "references/surface.md", marker: "- `surface_element`:"},
	"surface_cdp":        {file: "references/surface.md", marker: "- `surface_cdp`:"},
	"awcp_manual":        {file: "references/awcp.md", marker: "## Read The Directory"},
	"awcp_invoke":        {file: "references/awcp.md", marker: "## Invoke The Selected Action"},
}

// Native connector tools only name a capability and point at the connector
// skill. Field-name coverage detects omissions; it does not validate semantics
// or prove that a model reads the skill before invoking a tool.
func TestNativeConnectorToolsDeferToTheirSkill(t *testing.T) {
	const maxDescription, maxPropertyDescription = 160, 120

	manuals := map[string]string{}
	for _, id := range connector.NativeConnectorIDs() {
		skill := strings.TrimPrefix(id, "builtin.")
		root := "connectors/" + id + "/skills/" + skill
		data, err := resources.ConnectorFS.ReadFile(path.Join(root, "SKILL.md"))
		if err != nil {
			t.Fatalf("read %s skill: %v", skill, err)
		}
		manuals[id] = string(data)
	}

	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tool definitions: %v", err)
	}
	checked := 0
	for _, def := range defs {
		id, ok := connector.NativeToolConnector(def.Name)
		if !ok {
			continue
		}
		checked++
		skill := strings.TrimPrefix(id, "builtin.")
		if pointer := "Read the " + skill + " skill before use."; !strings.HasSuffix(def.Description, pointer) {
			t.Errorf("%s description must end with %q, got %q", def.Name, pointer, def.Description)
		}
		if len(def.Description) > maxDescription {
			t.Errorf("%s description is %d characters; move usage details into the %s skill", def.Name, len(def.Description), skill)
		}
		for name, raw := range contracts.AnyMapNode(def.Parameters["properties"]) {
			description, _ := contracts.AnyMapNode(raw)["description"].(string)
			if len(description) > maxPropertyDescription {
				t.Errorf("%s.%s description is %d characters; move usage details into the %s skill", def.Name, name, len(description), skill)
			}
		}
		if !strings.Contains(manuals[id], def.Name) {
			t.Errorf("%s skill entry does not list %s", skill, def.Name)
		}
		chapter, ok := nativeToolManualChapters[def.Name]
		if !ok {
			t.Errorf("%s has no manual chapter mapping", def.Name)
			continue
		}
		if chapter.file != "SKILL.md" && !strings.Contains(manuals[id], chapter.file) {
			t.Errorf("%s skill entry does not link %s", skill, chapter.file)
		}
		root := "connectors/" + id + "/skills/" + skill
		data, err := resources.ConnectorFS.ReadFile(path.Join(root, chapter.file))
		if err != nil {
			t.Errorf("read %s manual chapter: %v", def.Name, err)
			continue
		}
		text, found := nativeManualChapterText(string(data), chapter.marker)
		if !found {
			t.Errorf("%s manual chapter marker %q is missing from %s", def.Name, chapter.marker, chapter.file)
			continue
		}
		for _, field := range missingNativeManualFields(def.Parameters, text) {
			t.Errorf("%s schema field %s is missing from %s (%s)", def.Name, field, chapter.file, chapter.marker)
		}
	}
	if checked != 32 {
		t.Fatalf("checked %d native connector tools, want 32", checked)
	}
}

func nativeManualChapterText(text, marker string) (string, bool) {
	if marker == "" {
		return text, true
	}
	lines := strings.Split(text, "\n")
	for start, line := range lines {
		if !strings.HasPrefix(line, marker) {
			continue
		}
		for end := start + 1; end < len(lines); end++ {
			if strings.HasPrefix(lines[end], "## ") || strings.HasPrefix(lines[end], "- `") {
				return strings.Join(lines[start:end], "\n"), true
			}
		}
		return strings.Join(lines[start:], "\n"), true
	}
	return "", false
}

var nativeManualIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func missingNativeManualFields(schema map[string]any, text string) []string {
	words := map[string]bool{}
	for _, word := range nativeManualIdentifier.FindAllString(text, -1) {
		words[word] = true
	}
	var missing []string
	var walk func(map[string]any, string)
	walk = func(node map[string]any, prefix string) {
		for name, raw := range contracts.AnyMapNode(node["properties"]) {
			field := prefix + name
			if !words[name] {
				missing = append(missing, field)
			}
			walk(contracts.AnyMapNode(raw), field+".")
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(items, prefix+"[].")
		}
	}
	walk(schema, "")
	sort.Strings(missing)
	return missing
}

func TestNativeConnectorManualCoverageDetectsOmissions(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tool, name, field string }{
		{"chat_start", "mustUseSkills", "mustUseSkills"},
		{"surface_navigate", "ignoreCache", "ignoreCache"},
		{"automation_manage", "hidden", "args.query.hidden"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			for _, def := range defs {
				if def.Name != tc.tool {
					continue
				}
				id, _ := connector.NativeToolConnector(def.Name)
				root := "connectors/" + id + "/skills/" + strings.TrimPrefix(id, "builtin.")
				chapter := nativeToolManualChapters[def.Name]
				data, err := resources.ConnectorFS.ReadFile(path.Join(root, chapter.file))
				if err != nil {
					t.Fatal(err)
				}
				// Keeping the name as a longer identifier must not hide an omission.
				altered := regexp.MustCompile(`\b`+tc.name+`\b`).ReplaceAllString(string(data), tc.name+"Removed")
				if chapter.marker != "" {
					// Mentioning it under a neighboring tool must not count either.
					altered += "\n- `another_tool`: " + tc.name + "\n"
				}
				text, found := nativeManualChapterText(altered, chapter.marker)
				if !found {
					t.Fatal("missing manual chapter")
				}
				missing := missingNativeManualFields(def.Parameters, text)
				for _, field := range missing {
					if field == tc.field {
						return
					}
				}
				t.Fatalf("omitted field %s was not detected: %v", tc.field, missing)
			}
			t.Fatalf("tool %s was not found", tc.tool)
		})
	}
}
