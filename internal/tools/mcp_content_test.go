package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

func TestNormalizeMCPResultRetainsOrderedContentBlocks(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "text", "text": "first paragraph"},
		map[string]any{"type": "resource_link", "uri": "mcp://docs/source", "name": "source", "annotations": map[string]any{"audience": []any{"user"}}, "_meta": map[string]any{"revision": json.Number("9007199254740993")}},
		map[string]any{"type": "text", "text": "last paragraph"},
	}
	result := normalizeMCPResult("search", map[string]any{"content": blocks})
	want := "first paragraph\n" + MarshalJSON(blocks[1]) + "\nlast paragraph"
	if result.Output != want || result.ExitCode != 0 || !reflect.DeepEqual(result.ContentBlocks, blocks) {
		t.Fatalf("ordered content was lost: %#v", result)
	}
	result.ContentBlocks[1].(map[string]any)["name"] = "changed by consumer"
	if blocks[1].(map[string]any)["name"] != "source" {
		t.Fatal("retained content aliases the upstream payload")
	}
}

func TestNormalizeMCPResultKeepsBinaryContentOutOfModelText(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "image", "mimeType": "image/png", "data": "image-base64-body", "_meta": map[string]any{"source": "preview"}},
		map[string]any{"type": "audio", "mimeType": "audio/wav", "data": "audio-base64-body"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "mcp://export/file", "mimeType": "application/pdf", "blob": "pdf-base64-body"}},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "mcp://export/readme", "text": "embedded readable text"}},
	}
	result := normalizeMCPResult("export", map[string]any{"content": blocks})
	for _, body := range []string{"image-base64-body", "audio-base64-body", "pdf-base64-body"} {
		if strings.Contains(result.Output, body) {
			t.Fatalf("binary content entered model text: %s", result.Output)
		}
	}
	for _, detail := range []string{"image/png", "audio/wav", "mcp://export/file", "embedded readable text", `"dataOmitted":true`, `"blobOmitted":true`, `"source":"preview"`} {
		if !strings.Contains(result.Output, detail) {
			t.Fatalf("missing content metadata %q: %s", detail, result.Output)
		}
	}
	if !reflect.DeepEqual(result.ContentBlocks, blocks) || result.Structured["data"] != "image-base64-body" {
		t.Fatal("model projection mutated retained blocks or legacy single-block result")
	}
}

func TestMCPContentBlocksLeaveStructuredOutputSchemaUntouched(t *testing.T) {
	structured := map[string]any{"published": json.Number("1700000000000"), "contentBlocks": "business field"}
	blocks := []any{
		map[string]any{"type": "text", "text": "first note"},
		map[string]any{"type": "text", "text": "second note"},
	}
	def := api.ToolDetailResponse{
		Name: "remote_publish", Meta: map[string]any{"sourceType": "mcp", "serverKey": "remote"},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"published": map[string]any{"x-platform-time": timecontract.OutputSchemaEpochMillis},
		}},
	}
	router := mustNewToolRouter(t, stubBackendToolExecutor{}, outputSchemaMCPClient{payload: map[string]any{"structuredContent": structured, "content": blocks}}, outputSchemaToolCatalog{def: def}, nil)
	result, err := router.Invoke(context.Background(), def.Name, nil, &ExecutionContext{})
	if err != nil || result.Error != "" || !reflect.DeepEqual(result.Structured, structured) || !reflect.DeepEqual(result.ContentBlocks, blocks) {
		t.Fatalf("content changed business schema: result=%#v error=%v", result, err)
	}
	if result.Output != MarshalJSON(structured)+"\n\nfirst note\nsecond note" {
		t.Fatalf("model did not get supplemental text: %s", result.Output)
	}
}

func TestNormalizeMCPResultErrorRetainsAllDetailsAndBusinessCode(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "text", "text": "Digest is required."},
		map[string]any{"type": "text", "text": "Read the current resource before retrying."},
		map[string]any{"type": "resource_link", "uri": "mcp://docs/editing", "name": "editing manual"},
	}
	for _, structured := range []map[string]any{nil, {"code": "last_digest_required", "message": "digest is required", "retryable": false}} {
		payload := map[string]any{"isError": true, "content": blocks}
		if structured != nil {
			payload["structuredContent"] = structured
		}
		result := normalizeMCPResult("edit", payload)
		wantCode := "mcp_tool_error"
		if structured != nil {
			wantCode = "last_digest_required"
			if result.Structured["message"] != structured["message"] || result.Structured["retryable"] != false {
				t.Fatalf("business error fields changed: %#v", result.Structured)
			}
		} else if result.Structured["message"] != "Digest is required.\nRead the current resource before retrying." {
			t.Fatalf("error message lost text blocks: %#v", result.Structured)
		}
		if result.Error != wantCode || result.ExitCode == 0 || !reflect.DeepEqual(result.ContentBlocks, blocks) {
			t.Fatalf("error code or blocks lost: %#v", result)
		}
		for _, detail := range []string{"Digest is required.", "Read the current resource before retrying.", "mcp://docs/editing"} {
			if !strings.Contains(result.Output, detail) {
				t.Fatalf("error model output lost %q: %s", detail, result.Output)
			}
		}
	}
}

func TestNormalizeMCPResultSingleAndEmptyContentCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
		output  string
		code    string
	}{
		{"single text", map[string]any{"content": []any{map[string]any{"type": "text", "text": "original"}}}, "original", ""},
		{"empty text", map[string]any{"content": []any{map[string]any{"type": "text", "text": ""}}}, "", ""},
		{"empty array", map[string]any{"content": []any{}}, `{"content":[]}`, ""},
		{"no content", map[string]any{"value": 1}, `{"value":1}`, ""},
		{"structured fallback duplicate", map[string]any{"structuredContent": map[string]any{"value": 1}, "content": []any{map[string]any{"type": "text", "text": `{"value":1}`}}}, `{"value":1}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeMCPResult("tool", tc.payload)
			if result.Output != tc.output || result.Error != tc.code {
				t.Fatalf("old result changed: %#v", result)
			}
		})
	}
	if result := normalizeMCPResult("tool", nil); result.Error != "mcp_empty_result" || result.ExitCode == 0 {
		t.Fatalf("nil result compatibility changed: %#v", result)
	}
	// Retained blocks remain JSON serializable for tool streams and Chat history.
	if _, err := json.Marshal(normalizeMCPResult("tool", map[string]any{"content": []any{map[string]any{"type": "text", "text": "original"}}})); err != nil {
		t.Fatal(err)
	}
}
