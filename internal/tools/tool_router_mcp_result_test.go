package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func mcpText(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func TestNormalizeMCPResultKeepsAllContent(t *testing.T) {
	link := map[string]any{"type": "resource_link", "uri": "file:///r.txt", "name": "r.txt"}
	for _, tc := range []struct {
		name           string
		payload        map[string]any
		wantOutput     string
		wantStructured map[string]any
	}{
		{
			name:       "T01 multiple text blocks",
			payload:    map[string]any{"content": []any{mcpText("查询摘要"), mcpText("详细结果")}},
			wantOutput: "查询摘要\n\n详细结果",
		},
		{
			name: "T02 structured plus supplementary text",
			payload: map[string]any{
				"structuredContent": map[string]any{"count": json.Number("2")},
				"content":           []any{mcpText("部分数据源查询失败")},
			},
			wantOutput:     "部分数据源查询失败\n\nstructuredContent:\n{\"count\":2}",
			wantStructured: map[string]any{"count": json.Number("2")},
		},
		{
			name: "T03 empty structured does not hide text",
			payload: map[string]any{
				"structuredContent": map[string]any{},
				"content":           []any{mcpText("实际查询结果")},
			},
			wantOutput:     "实际查询结果",
			wantStructured: map[string]any{},
		},
		{
			name:           "T04 text then resource link",
			payload:        map[string]any{"content": []any{mcpText("见附件"), link}},
			wantOutput:     "见附件\n\n{\"name\":\"r.txt\",\"type\":\"resource_link\",\"uri\":\"file:///r.txt\"}",
			wantStructured: map[string]any{"content": []any{mcpText("见附件"), link}},
		},
		{
			name:           "T05 resource link then text",
			payload:        map[string]any{"content": []any{link, mcpText("解释")}},
			wantOutput:     "{\"name\":\"r.txt\",\"type\":\"resource_link\",\"uri\":\"file:///r.txt\"}\n\n解释",
			wantStructured: map[string]any{"content": []any{link, mcpText("解释")}},
		},
		{
			name:       "T06 empty first text block",
			payload:    map[string]any{"content": []any{mcpText(""), mcpText("实际查询结果")}},
			wantOutput: "实际查询结果",
		},
		{
			name:       "single text block unchanged",
			payload:    map[string]any{"content": []any{mcpText("ok")}},
			wantOutput: "ok",
		},
		{
			name:           "structured only unchanged",
			payload:        map[string]any{"structuredContent": map[string]any{"count": json.Number("2")}},
			wantOutput:     `{"count":2}`,
			wantStructured: map[string]any{"count": json.Number("2")},
		},
		{
			name: "structured with spec duplicate text unchanged",
			payload: map[string]any{
				"structuredContent": map[string]any{"count": json.Number("2"), "items": []any{"a"}},
				"content":           []any{mcpText("{\n  \"items\": [\"a\"],\n  \"count\": 2\n}")},
			},
			wantOutput:     `{"count":2,"items":["a"]}`,
			wantStructured: map[string]any{"count": json.Number("2"), "items": []any{"a"}},
		},
		{
			name: "structured duplicate plus extra text keeps both without repeating",
			payload: map[string]any{
				"structuredContent": map[string]any{"count": json.Number("2")},
				"content":           []any{mcpText(`{"count":2}`), mcpText("结果不完整")},
			},
			wantOutput:     "{\"count\":2}\n\n结果不完整",
			wantStructured: map[string]any{"count": json.Number("2")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeMCPResult("mcp-tool", tc.payload)
			if result.ExitCode != 0 || result.Error != "" {
				t.Fatalf("unexpected failure: %#v", result)
			}
			if result.Output != tc.wantOutput {
				t.Fatalf("output mismatch\nwant %q\ngot  %q", tc.wantOutput, result.Output)
			}
			if !reflect.DeepEqual(result.Structured, tc.wantStructured) {
				t.Fatalf("structured mismatch\nwant %#v\ngot  %#v", tc.wantStructured, result.Structured)
			}
		})
	}
}

func TestNormalizeMCPResultElidesInlineBinaryForModel(t *testing.T) {
	image := map[string]any{"type": "image", "mimeType": "image/png", "data": strings.Repeat("A", 16)}
	blob := map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///a.bin", "blob": strings.Repeat("B", 8)}}
	items := []any{mcpText("截图如下"), image, blob}
	result := normalizeMCPResult("mcp-tool", map[string]any{"content": items})
	want := "截图如下\n\n" +
		`{"data":"[base64 omitted: 16 chars]","mimeType":"image/png","type":"image"}` + "\n\n" +
		`{"resource":{"blob":"[base64 omitted: 8 chars]","uri":"file:///a.bin"},"type":"resource"}`
	if result.Output != want {
		t.Fatalf("output mismatch\nwant %q\ngot  %q", want, result.Output)
	}
	got := result.Structured["content"].([]any)
	if got[1].(map[string]any)["data"] != strings.Repeat("A", 16) || got[2].(map[string]any)["resource"].(map[string]any)["blob"] != strings.Repeat("B", 8) {
		t.Fatalf("structured content must keep the original blocks: %#v", got)
	}
}

func TestNormalizeMCPResultKeepsTextBlockMetadata(t *testing.T) {
	block := map[string]any{"type": "text", "text": "hi", "annotations": map[string]any{"audience": []any{"user"}}}
	result := normalizeMCPResult("mcp-tool", map[string]any{"content": []any{block}})
	if result.Output != "hi" {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if !reflect.DeepEqual(result.Structured, map[string]any{"content": []any{block}}) {
		t.Fatalf("annotations must be preserved: %#v", result.Structured)
	}
}

func TestNormalizeMCPErrorResultKeepsDiagnostics(t *testing.T) {
	t.Run("T07 two error text blocks", func(t *testing.T) {
		result := normalizeMCPResult("qs_edit", map[string]any{
			"isError": true,
			"content": []any{mcpText("参数错误"), mcpText("请补充 digest 参数，然后重新调用")},
		})
		if result.Error != "mcp_tool_error" || result.ExitCode != -1 {
			t.Fatalf("unexpected status: %#v", result)
		}
		if result.Structured["message"] != "参数错误" || result.Structured["mcpContent"] != "参数错误\n\n请补充 digest 参数，然后重新调用" {
			t.Fatalf("diagnostics lost: %#v", result.Structured)
		}
		if !strings.Contains(result.Output, "请补充 digest 参数") {
			t.Fatalf("model output lost diagnostics: %s", result.Output)
		}
	})
	t.Run("T08 structured error plus fix hint", func(t *testing.T) {
		result := normalizeMCPResult("qs_edit", map[string]any{
			"isError":           true,
			"structuredContent": map[string]any{"error": "digest_required", "code": "E42", "message": "digest is required"},
			"content":           []any{mcpText("先调用 qs_read 获取 digest")},
		})
		if result.Error != "digest_required" || result.Structured["code"] != "digest_required" {
			t.Fatalf("business error code rule changed: %#v", result)
		}
		if result.Structured["mcpContent"] != "先调用 qs_read 获取 digest" {
			t.Fatalf("fix hint lost: %#v", result.Structured)
		}
		original, _ := result.Structured["mcpStructuredContent"].(map[string]any)
		if original["code"] != "E42" {
			t.Fatalf("overwritten server code must be preserved: %#v", result.Structured)
		}
		if !strings.Contains(result.Output, "先调用 qs_read") || !strings.Contains(result.Output, "E42") {
			t.Fatalf("model output lost content: %s", result.Output)
		}
	})
	t.Run("first non-empty text is the message", func(t *testing.T) {
		result := normalizeMCPResult("qs_edit", map[string]any{
			"isError": true,
			"content": []any{mcpText(" "), mcpText("真正的错误")},
		})
		if result.Structured["message"] != "真正的错误" {
			t.Fatalf("unexpected message: %#v", result.Structured)
		}
		if _, ok := result.Structured["mcpContent"]; ok {
			t.Fatalf("content equal to message must not be repeated: %#v", result.Structured)
		}
	})
}

func TestToolRouterMCPResultReachesModelOutput(t *testing.T) {
	def := api.ToolDetailResponse{Name: "mcp-result", Meta: map[string]any{"sourceType": "mcp", "serverKey": "remote"}}
	router := mustNewToolRouter(
		t,
		stubBackendToolExecutor{},
		outputSchemaMCPClient{payload: map[string]any{
			"structuredContent": map[string]any{"count": json.Number("2")},
			"content":           []any{mcpText("部分数据源查询失败")},
		}},
		outputSchemaToolCatalog{def: def},
		nil,
	)
	result, err := router.Invoke(context.Background(), def.Name, nil, nil)
	if err != nil || result.Error != "" {
		t.Fatalf("unexpected failure: result=%#v err=%v", result, err)
	}
	if !strings.Contains(result.Output, "部分数据源查询失败") || !strings.Contains(result.Output, `{"count":2}`) {
		t.Fatalf("model output must carry content and structured data: %q", result.Output)
	}
	if !reflect.DeepEqual(result.Structured, map[string]any{"count": json.Number("2")}) {
		t.Fatalf("structured root must stay the business payload: %#v", result.Structured)
	}
}
