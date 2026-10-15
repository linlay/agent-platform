package mcp

import (
	"testing"

	"agent-platform/internal/api"
)

func gatewayDef(connectorID, component, name, label, description string, properties map[string]any) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name:        "mcp_x_" + name,
		Label:       label,
		Description: description,
		Parameters:  map[string]any{"type": "object", "properties": properties},
		Meta:        map[string]any{"sourceType": "mcp", "connectorId": connectorID, "mcpServer": component, "mcpToolName": name},
	}
}

func TestGatewayToolIDRoundTrip(t *testing.T) {
	def := gatewayDef("dingteam-suite", "dingteam-okr", "list_objectives", "", "", nil)
	id, ok := GatewayToolID(def)
	if !ok || id != "dingteam-suite/dingteam-okr/list_objectives" {
		t.Fatalf("id = %q %v", id, ok)
	}
	connectorID, component, name, ok := ParseGatewayToolID(id)
	if !ok || connectorID != "dingteam-suite" || component != "dingteam-okr" || name != "list_objectives" {
		t.Fatalf("parse = %q %q %q %v", connectorID, component, name, ok)
	}
	if _, _, name, ok := ParseGatewayToolID("kdocs/main/files/list"); !ok || name != "files/list" {
		t.Fatalf("slash in remote name = %q %v", name, ok)
	}
	for _, invalid := range []string{"", "kdocs", "kdocs/create_document", "kdocs//create", " / / "} {
		if _, _, _, ok := ParseGatewayToolID(invalid); ok {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if !MatchesGatewayToolID(def, "DingTeam-Suite/dingteam-okr/list_objectives") {
		t.Fatal("connector and server match case-insensitively")
	}
	if _, ok := GatewayToolID(api.ToolDetailResponse{Name: "local", Meta: map[string]any{}}); ok {
		t.Fatal("tool without connector provenance has no gateway id")
	}
}

func TestComponentNameFromServerKey(t *testing.T) {
	for _, tc := range []struct{ connector, source, want string }{
		{"kdocs", "kdocs", "main"},
		{"kdocs", "", "main"},
		{"dingteam-suite", "dingteam-suite.dingteam-okr", "dingteam-okr"},
		{"kdocs", "other.thing", ""},
		{"", "kdocs", ""},
	} {
		if got := ComponentName(tc.connector, tc.source); got != tc.want {
			t.Fatalf("ComponentName(%q, %q) = %q, want %q", tc.connector, tc.source, got, tc.want)
		}
	}
}

func TestSearchToolsRanksChineseAndExactNames(t *testing.T) {
	defs := []api.ToolDetailResponse{
		gatewayDef("kdocs", "main", "create_document", "创建文档", "Create a new cloud document in a folder.", map[string]any{
			"title": map[string]any{"type": "string", "description": "文档标题"},
		}),
		gatewayDef("kdocs", "main", "search_files", "搜索文件", "Search files by keyword.", nil),
		gatewayDef("tencent-map", "main", "geocode", "地址解析", "Convert an address to coordinates.", nil),
		gatewayDef("kdocs", "main", "delete_document", "删除文档", "Delete a document permanently.", nil),
		{Name: "no_provenance", Description: "创建文档", Meta: map[string]any{}},
	}
	hits := SearchTools(defs, "创建文档", 3)
	if len(hits) == 0 || hits[0].Meta["mcpToolName"] != "create_document" {
		t.Fatalf("chinese hits = %#v", hits)
	}
	for _, hit := range hits {
		if hit.Name == "no_provenance" {
			t.Fatal("tools without gateway id must not be returned")
		}
	}
	if hits := SearchTools(defs, "createDocument", 1); len(hits) != 1 || hits[0].Meta["mcpToolName"] != "create_document" {
		t.Fatalf("camel case = %#v", hits)
	}
	// An exact remote name outranks a description that repeats the words.
	if hits := SearchTools(defs, "geocode", 5); len(hits) != 1 || hits[0].Meta["mcpToolName"] != "geocode" {
		t.Fatalf("exact = %#v", hits)
	}
	if hits := SearchTools(defs, "kdocs/main/search_files", 5); len(hits) == 0 || hits[0].Meta["mcpToolName"] != "search_files" {
		t.Fatalf("tool id query = %#v", hits)
	}
	if hits := SearchTools(defs, "document", 1); len(hits) != 1 {
		t.Fatalf("limit = %#v", hits)
	}
	if hits := SearchTools(defs, "weather forecast", 5); len(hits) != 0 {
		t.Fatalf("unrelated query matched %#v", hits)
	}
	if hits := SearchTools(defs, "  ", 5); hits != nil {
		t.Fatalf("blank query matched %#v", hits)
	}
}
