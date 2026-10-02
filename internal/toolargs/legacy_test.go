package toolargs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemovedArgumentsRejectedWithoutMutatingInput(t *testing.T) {
	cases := []struct{ tool, raw, replacement string }{
		{"file_read", `{"file_path":"secret"}`, "filePath"},
		{"file_read", `{"filePath":"secret","add_line_numbers":false}`, "addLineNumbers"},
		{"file_write", `{"filePath":"new","file_path":"old","content":"secret"}`, "filePath"},
		{"file_edit", `{"old_string":"secret"}`, "oldString"},
		{"file_edit", `{"new_string":"secret"}`, "newString"},
		{"file_edit", `{"replace_all":false}`, "replaceAll"},
		{"file_glob", `{"head_limit":0}`, "headLimit"},
		{"kbase_files", `{"head_limit":0}`, "headLimit"},
		{"file_grep", `{"head_limit":0}`, "headLimit"},
		{"file_grep", `{"output_mode":"count"}`, "outputMode"},
		{"file_grep", `{"-i":false}`, "caseInsensitive"},
		{"file_grep", `{"-n":false}`, "lineNumbers"},
		{"file_grep", `{"-A":0}`, "afterContext"},
		{"file_grep", `{"-B":0}`, "beforeContext"},
		{"file_grep", `{"-C":0}`, "context"},
		{"regex", `{"case_insensitive":false}`, "caseInsensitive"},
		{"wait", `{"duration_ms":1}`, "offset"},
		{"image_generate", `{"response_format":"url"}`, "responseFormat"},
		{"image_generate", `{"images":[{"source_type":"secret"}]}`, "images[0].sourceType"},
		{"image_generate", `{"mask":{"source_type":"secret"}}`, "mask.sourceType"},
		{"image_generate", `{"images":[{"sourceType":"reference_name"}]}`, "referenceName"},
		{"image_generate", `{"mask":{"sourceType":"file_path"}}`, "filePath"},
		{"vision_recognize", `{"images":[{}, {"file_path":"secret"}]}`, "images[1].filePath"},
		{"vision_recognize", `{"images":[{"reference_name":"secret"}]}`, "referenceName"},
		{"vision_recognize", `{"output_format":"text"}`, "outputFormat"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+tc.replacement, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &args); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(args)
			for i := 0; i < 2; i++ {
				err := RejectLegacy(tc.tool, args)
				if err == nil || !strings.Contains(err.Error(), tc.replacement) || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			after, _ := json.Marshal(args)
			if string(before) != string(after) {
				t.Fatal("arguments mutated")
			}
		})
	}
}

func TestCanonicalAndOpaqueArgumentsPass(t *testing.T) {
	for _, tc := range []struct{ tool, raw string }{
		{"file_read", `{"filePath":"file_path","addLineNumbers":false}`},
		{"regex", `{"caseInsensitive":true,"text":"case_insensitive"}`},
		{"image_generate", `{"images":[{"sourceType":"referenceName","value":"image.png"}],"mask":{"sourceType":"filePath","value":"@chat/mask.png","mode":"white_edit"},"responseFormat":"b64_json"}`},
		{"vision_recognize", `{"images":[{"referenceName":"image.png"}],"outputFormat":"text"}`},
		{"desktop_cdp", `{"method":"AWCP.invoke","params":{"args":{"file_path":"opaque"}}}`},
		{"desktop_action", `{"args":{"source_type":"opaque"}}`},
		{"mcp_external", `{"file_path":"opaque","response_format":"url"}`},
	} {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.raw), &args)
		if err := RejectLegacy(tc.tool, args); err != nil {
			t.Fatal(tc.tool, err)
		}
	}
}
