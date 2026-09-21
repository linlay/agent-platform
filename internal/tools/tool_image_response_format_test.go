package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/contracts"
)

func TestImageGenerateResponseFormatPolicy(t *testing.T) {
	for _, edit := range []bool{false, true} {
		for _, omit := range []bool{false, true} {
			for _, actual := range []string{"b64_json", "url"} {
				t.Run(fmt.Sprintf("edit=%v/omit=%v/actual=%s", edit, omit, actual), func(t *testing.T) {
					raw := testPNGBytes(t, 1, 1, nil)
					var captured map[string]any
					var server *httptest.Server
					server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/asset.png" {
							w.Header().Set("Content-Type", "image/png")
							_, _ = w.Write(raw)
							return
						}
						captured = map[string]any{}
						if edit {
							if err := r.ParseMultipartForm(1 << 20); err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							defer r.MultipartForm.RemoveAll()
							for k, v := range r.MultipartForm.Value {
								captured[k] = v[0]
							}
							if len(r.MultipartForm.File["image[]"]) != 1 {
								t.Error("missing edit image")
							}
						} else if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
							t.Error(err)
							w.WriteHeader(400)
							return
						}
						item := map[string]string{"b64_json": base64.StdEncoding.EncodeToString(raw)}
						if actual == "url" {
							item = map[string]string{"url": server.URL + "/asset.png"}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{item}})
					}))
					defer server.Close()
					// Opposite settings prove generation and editing are independent.
					generationOmit, editOmit := omit, !omit
					if edit {
						generationOmit, editOmit = !omit, omit
					}
					registry := writeImageGenerateRegistryWithImageConfig(t, server.URL, true, []string{
						"  responseFormats: [b64_json, url]",
						"  generation:", "    endpointPath: /generate", "    requestFormat: openai-images-json", fmt.Sprintf("    omitResponseFormat: %v", generationOmit),
						"  edit:", "    endpointPath: /edit", "    requestFormat: openai-images-multipart", fmt.Sprintf("    omitResponseFormat: %v", editOmit),
						// Model compat must not reintroduce an omitted parameter.
						"compat:", "  request:", "    always:", "      response_format: b64_json",
					})
					root := t.TempDir()
					chatDir := filepath.Join(root, "chat-1")
					if err := os.MkdirAll(chatDir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(chatDir, "input.png"), raw, 0600); err != nil {
						t.Fatal(err)
					}
					args := map[string]any{"prompt": "draw", "response_format": "url"}
					if edit {
						args["images"] = []any{map[string]any{"source_type": "reference_name", "value": "input.png"}}
					}
					executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, root)
					result, err := executor.invokeImageGenerate(context.Background(), args, &contracts.ExecutionContext{Session: contracts.QuerySession{
						ChatID: "chat-1", RunID: "run-1", RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{ChatDir: chatDir}},
					}})
					if err != nil || result.Error != "" {
						t.Fatalf("invoke: %v %#v", err, result)
					}
					_, present := captured["response_format"]
					if present == omit {
						t.Fatalf("response_format presence=%v, omit=%v", present, omit)
					}
					if present {
						want := "b64_json"
						if edit {
							want = "url"
						}
						if captured["response_format"] != want {
							t.Fatalf("unexpected format: %v", captured)
						}
					}
					if result.Structured["responseFormat"] != actual {
						t.Fatalf("reported format: %v", result.Structured["responseFormat"])
					}
					images := result.Structured["images"].([]map[string]any)
					saved, err := os.ReadFile(images[0]["path"].(string))
					if err != nil || !bytes.Equal(saved, raw) {
						t.Fatalf("persisted bytes differ: %v", err)
					}
				})
			}
		}
	}
}

func TestActualImageGenerateResponseFormat(t *testing.T) {
	for _, tc := range []struct {
		items []imageGenerateData
		want  string
	}{
		{[]imageGenerateData{{B64JSON: "image"}}, "b64_json"},
		{[]imageGenerateData{{URL: "https://example.com/image", B64JSON: "image"}}, "url"},
		{[]imageGenerateData{{URL: "https://example.com/image"}, {B64JSON: "image"}}, "mixed"},
	} {
		if got := actualImageGenerateResponseFormat(tc.items); got != tc.want {
			t.Fatalf("got %s, want %s", got, tc.want)
		}
	}
}
