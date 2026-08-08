package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestImageGenerateDisabled(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_disabled" {
		t.Fatalf("expected disabled error, got %#v", result)
	}
}

func TestImageGenerateMissingProfile(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":  "draw",
		"profile": "missing",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_profile_not_found" {
		t.Fatalf("expected missing profile error, got %#v", result)
	}
}

func TestImageGenerateMissingModel(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", true)
	cfg := defaultImageGenerateTestConfig()
	cfg.Profiles["general"] = config.ImageGenerateProfileConfig{
		ModelKey:        "missing-model",
		Timeout:         120,
		Size:            "1024x1024",
		ResponseFormat:  "b64_json",
		OutputMimeType:  "image/png",
		MaxPromptChars:  4000,
		PersistArtifact: true,
	}
	executor := imageGenerateTestExecutor(cfg, registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_not_found" {
		t.Fatalf("expected missing model error, got %#v", result)
	}
}

func TestImageGenerateProviderConfigInvalid(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", false)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_provider_config_invalid" {
		t.Fatalf("expected provider config error, got %#v", result)
	}
}

func TestImageGenerateRejectsNonImageModel(t *testing.T) {
	registry := writeImageGenerateRegistryWithType(t, "http://127.0.0.1:1", true, models.ModelTypeChat)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_not_image_generation" {
		t.Fatalf("expected model type error, got %#v", result)
	}
}

func TestImageGenerateUsesModelImageDefaults(t *testing.T) {
	var captured map[string]any
	var modelServer *httptest.Server
	modelServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("model image"))
			return
		}
		if r.URL.Path != "/custom/images" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"` + modelServer.URL + `/asset.png"}]}`))
	}))
	defer modelServer.Close()

	registry := writeImageGenerateRegistryWithImageConfig(t, modelServer.URL, true, []string{
		"  endpointPath: /custom/images",
		"  timeout: 3",
		"  defaultSize: 768x768",
		"  responseFormats:",
		"    - url",
	})
	cfg := defaultImageGenerateTestConfig()
	cfg.Profiles["general"] = config.ImageGenerateProfileConfig{
		ModelKey:        "image-model",
		ResponseFormat:  "url",
		OutputMimeType:  "image/png",
		MaxPromptChars:  4000,
		PersistArtifact: true,
	}
	executor := imageGenerateTestExecutor(cfg, registry, t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if captured["size"] != "768x768" || captured["response_format"] != "url" {
		t.Fatalf("expected model image defaults in request, got %#v", captured)
	}
}

func TestImageGenerateB64ResponsePersistsArtifact(t *testing.T) {
	imageBytes := []byte("fake image bytes")
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	var captured map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"` + encoded + `","revised_prompt":"clearer prompt"}],"usage":{"total_tokens":7}}`))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":          "draw a tiny robot",
		"size":            "512x512",
		"response_format": "b64_json",
		"n":               2,
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if captured["model"] != "image-model-id" ||
		captured["prompt"] != "draw a tiny robot" ||
		captured["size"] != "512x512" ||
		captured["response_format"] != "b64_json" ||
		captured["n"] != float64(2) {
		t.Fatalf("unexpected request body: %#v", captured)
	}
	images, ok := result.Structured["images"].([]map[string]any)
	if !ok || len(images) != 1 {
		t.Fatalf("expected one image result, got %#v", result.Structured["images"])
	}
	image := images[0]
	path := contracts.AnyStringNode(image["path"])
	if path == "" || filepath.Dir(path) != filepath.Join(chatsRoot, "chat-1") {
		t.Fatalf("expected persisted image in chat root, got %#v", image)
	}
	filename := filepath.Base(path)
	if !strings.HasPrefix(filename, "image_generate_run-1_") {
		t.Fatalf("expected filename to include run ID, got %q", filename)
	}
	relativePath := contracts.AnyStringNode(image["relativePath"])
	if relativePath != filename || strings.Contains(relativePath, "/") {
		t.Fatalf("expected root relative path, got %#v", image)
	}
	resourceURL := contracts.AnyStringNode(image["url"])
	if resourceURL != filename {
		t.Fatalf("expected URL to target chat root file, got %q", resourceURL)
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1")); !os.IsNotExist(err) {
		t.Fatalf("did not expect artifact directory, stat err=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted artifact: %v", err)
	}
	if string(data) != string(imageBytes) {
		t.Fatalf("unexpected persisted artifact bytes: %q", string(data))
	}
	sum := sha256.Sum256(imageBytes)
	if image["sha256"] != hex.EncodeToString(sum[:]) ||
		image["mimeType"] != "image/png" ||
		image["sizeBytes"] != len(imageBytes) ||
		image["revisedPrompt"] != "clearer prompt" ||
		strings.HasPrefix(resourceURL, "/") {
		t.Fatalf("unexpected image metadata: %#v", image)
	}
	if result.Structured["rawCreated"] != int64(123) {
		t.Fatalf("expected rawCreated, got %#v", result.Structured)
	}

	publishedResult := publishArtifacts(chatsRoot, "chat-1", "run-1", "", []any{
		map[string]any{"path": path},
	})
	if publishedResult.Status != "published" || len(publishedResult.PublishedArtifacts) != 1 {
		t.Fatalf("expected generated image to publish, got %#v", publishedResult)
	}
	published := publishedResult.PublishedArtifacts[0]
	wantPublishedURL := filepath.ToSlash(filepath.Join("artifacts", "run-1", filename))
	if published["url"] != wantPublishedURL {
		t.Fatalf("published URL=%#v want=%q", published["url"], wantPublishedURL)
	}
	if published["url"] == resourceURL {
		t.Fatalf("published artifact must use its copied URL, source URL=%q", resourceURL)
	}
	publishedBytes, err := os.ReadFile(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1", filename))
	if err != nil || !bytes.Equal(publishedBytes, imageBytes) {
		t.Fatalf("read published copy: err=%v bytes=%q", err, publishedBytes)
	}
}

func TestImageGenerateURLResponsePersistsArtifact(t *testing.T) {
	imageBytes := []byte("downloaded image bytes")
	var modelServer *httptest.Server
	modelServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBytes)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"url":"` + modelServer.URL + `/image.png","revised_prompt":"cdn prompt"}]}`))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt":          "draw",
		"response_format": "url",
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	images, ok := result.Structured["images"].([]map[string]any)
	if result.Error != "" || !ok || len(images) != 1 {
		t.Fatalf("expected URL image result, got %#v", result)
	}
	path := contracts.AnyStringNode(images[0]["path"])
	if path == "" || filepath.Dir(path) != filepath.Join(chatsRoot, "chat-1") {
		t.Fatalf("unexpected URL image metadata: %#v", images[0])
	}
	if images[0]["url"] != filepath.Base(path) || images[0]["revisedPrompt"] != "cdn prompt" {
		t.Fatalf("unexpected materialized URL image metadata: %#v", images[0])
	}
	persisted, err := os.ReadFile(path)
	if err != nil || string(persisted) != string(imageBytes) {
		t.Fatalf("unexpected downloaded image bytes=%q err=%v", persisted, err)
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1")); !os.IsNotExist(err) {
		t.Fatalf("did not expect artifact directory, stat err=%v", err)
	}
}

func TestImageGenerateRejectsEmptyData(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer modelServer.Close()

	registry := writeImageGenerateRegistry(t, modelServer.URL, true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, t.TempDir())
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "draw",
	}, &contracts.ExecutionContext{
		Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"},
	})
	if err != nil {
		t.Fatalf("invokeImageGenerate: %v", err)
	}
	if result.Error != "image_generate_model_response_invalid" {
		t.Fatalf("expected invalid response error, got %#v", result)
	}
}

func TestImageGenerateMultipartEditWithNormalizedMask(t *testing.T) {
	outputBytes := testPNGBytes(t, 2, 1, []color.NRGBA{{R: 1, A: 255}, {B: 2, A: 255}})
	var sawRequest bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "image-model-id" {
			t.Fatalf("model=%q", got)
		}
		if got := r.FormValue("prompt"); got != "move the robot" {
			t.Fatalf("prompt=%q", got)
		}
		if got := len(r.MultipartForm.File["image[]"]); got != 2 {
			t.Fatalf("image[] count=%d", got)
		}
		maskFiles := r.MultipartForm.File["mask"]
		if len(maskFiles) != 1 || maskFiles[0].Header.Get("Content-Type") != "image/png" {
			t.Fatalf("unexpected mask files: %#v", maskFiles)
		}
		file, err := maskFiles[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		maskBytes, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		decodedMask, err := png.Decode(bytes.NewReader(maskBytes))
		if err != nil {
			t.Fatalf("decode normalized mask: %v", err)
		}
		_, _, _, firstAlpha := decodedMask.At(0, 0).RGBA()
		_, _, _, secondAlpha := decodedMask.At(1, 0).RGBA()
		if firstAlpha != 0 || secondAlpha != 0xffff {
			t.Fatalf("normalized white_edit alpha=(%d,%d)", firstAlpha, secondAlpha)
		}
		_, _ = w.Write([]byte("{\"data\":[{\"b64_json\":\"" + base64.StdEncoding.EncodeToString(outputBytes) + "\"}]}"))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	chatDir := filepath.Join(chatsRoot, "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImageGenerateTestPNG(t, filepath.Join(chatDir, "target.png"), 2, 1, []color.NRGBA{{A: 255}, {A: 255}})
	writeImageGenerateTestPNG(t, filepath.Join(chatDir, "reference.png"), 2, 1, []color.NRGBA{{G: 10, A: 255}, {G: 20, A: 255}})
	writeImageGenerateTestPNG(t, filepath.Join(chatDir, "mask.png"), 2, 1, []color.NRGBA{{R: 255, G: 255, B: 255, A: 255}, {A: 255}})

	registry := writeImageGenerateRegistryWithImageConfig(t, modelServer.URL, true, []string{
		"  endpointPath: /v1/images/generations",
		"  responseFormats:",
		"    - b64_json",
		"  edit:",
		"    endpointPath: /v1/images/edits",
		"    requestFormat: openai-multipart",
		"    maskProtocol: openai-alpha",
	})
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "move the robot",
		"images": []any{
			map[string]any{"reference_name": "target.png"},
			map[string]any{"reference_name": "reference.png"},
		},
		"mask": map[string]any{"reference_name": "mask.png", "mode": "white_edit"},
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRequest || result.Error != "" || result.Structured["operation"] != "inpainting" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestImageGenerateChatCompletionEditUsesUnifiedInputs(t *testing.T) {
	outputBytes := testPNGBytes(t, 1, 1, []color.NRGBA{{R: 9, A: 255}})
	var captured map[string]any
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(outputBytes)
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"images\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"" + dataURL + "\"}}]}}],\"usage\":{\"total_tokens\":3}}"))
	}))
	defer modelServer.Close()

	chatsRoot := t.TempDir()
	chatDir := filepath.Join(chatsRoot, "chat-1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImageGenerateTestPNG(t, filepath.Join(chatDir, "target.png"), 1, 1, []color.NRGBA{{A: 255}})
	writeImageGenerateTestPNG(t, filepath.Join(chatDir, "reference.png"), 1, 1, []color.NRGBA{{B: 20, A: 255}})
	registry := writeImageGenerateRegistryWithImageConfig(t, modelServer.URL, true, []string{
		"  endpointPath: /v1/images/generations",
		"  responseFormats:",
		"    - b64_json",
		"  edit:",
		"    endpointPath: /v1/chat/completions",
		"    requestFormat: openai-chat-completions",
		"    maskProtocol: none",
	})
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, chatsRoot)
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "edit",
		"images": []any{
			map[string]any{"reference_name": "target.png"},
			map[string]any{"reference_name": "reference.png"},
		},
	}, &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Structured["operation"] != "edit" {
		t.Fatalf("unexpected result: %#v", result)
	}
	modalities, _ := captured["modalities"].([]any)
	if len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("modalities=%#v", captured["modalities"])
	}
	messages, _ := captured["messages"].([]any)
	content, _ := contracts.AnyMapNode(messages[0])["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content=%#v", content)
	}
}

func TestImageGenerateRejectsUnsupportedMaskBeforeReadingImages(t *testing.T) {
	registry := writeImageGenerateRegistryWithImageConfig(t, "http://127.0.0.1:1", true, []string{
		"  endpointPath: /v1/images/generations",
		"  edit:",
		"    endpointPath: /v1/chat/completions",
		"    requestFormat: openai-chat-completions",
		"    maskProtocol: none",
	})
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "edit",
		"images": []any{
			map[string]any{"reference_name": "missing.png"},
		},
		"mask": map[string]any{"reference_name": "missing-mask.png", "mode": "alpha"},
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "image_generate_mask_unsupported" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestImageGenerateMaskRequiresImages(t *testing.T) {
	registry := writeImageGenerateRegistry(t, "http://127.0.0.1:1", true)
	executor := imageGenerateTestExecutor(defaultImageGenerateTestConfig(), registry, "")
	result, err := executor.invokeImageGenerate(context.Background(), map[string]any{
		"prompt": "edit",
		"mask":   map[string]any{"reference_name": "mask.png", "mode": "alpha"},
	}, &contracts.ExecutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "image_generate_mask_requires_images" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDecodeGeneratedImageBase64SniffsActualJPEG(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	_, mimeType, err := decodeGeneratedImageBase64(base64.StdEncoding.EncodeToString(encoded.Bytes()), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" {
		t.Fatalf("mimeType=%q", mimeType)
	}
}

func TestDownloadGeneratedImageSniffsBytesBeforeHeader(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
	defer server.Close()
	executor := &RuntimeToolExecutor{httpClient: server.Client()}
	_, mimeType, err := executor.downloadGeneratedImage(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "image/jpeg" {
		t.Fatalf("mimeType=%q", mimeType)
	}
}

func TestNormalizeImageGenerateMaskModesAndDimensions(t *testing.T) {
	target := testPNGBytes(t, 2, 1, []color.NRGBA{{A: 255}, {A: 255}})
	tests := []struct {
		name      string
		mode      string
		pixels    []color.NRGBA
		wantAlpha [2]uint32
	}{
		{name: "alpha", mode: "alpha", pixels: []color.NRGBA{{A: 0}, {A: 255}}, wantAlpha: [2]uint32{0, 0xffff}},
		{name: "white edit", mode: "white_edit", pixels: []color.NRGBA{{R: 255, G: 255, B: 255, A: 255}, {A: 255}}, wantAlpha: [2]uint32{0, 0xffff}},
		{name: "black edit", mode: "black_edit", pixels: []color.NRGBA{{A: 255}, {R: 255, G: 255, B: 255, A: 255}}, wantAlpha: [2]uint32{0, 0xffff}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mask := testPNGBytes(t, 2, 1, test.pixels)
			converted, err := normalizeImageGenerateMask(target, mask, "image/png", test.mode)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(bytes.NewReader(converted))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, first := decoded.At(0, 0).RGBA()
			_, _, _, second := decoded.At(1, 0).RGBA()
			if first != test.wantAlpha[0] || second != test.wantAlpha[1] {
				t.Fatalf("alpha=(%d,%d), want=%v", first, second, test.wantAlpha)
			}
		})
	}
	wrongSize := testPNGBytes(t, 1, 1, []color.NRGBA{{A: 255}})
	if _, err := normalizeImageGenerateMask(target, wrongSize, "image/png", "alpha"); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("expected dimension mismatch, got %v", err)
	}
}

func TestImageGenerateDescriptionKeepsPathInternalAndRequiresReturnedURL(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "resources", "tools", "image_generate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	description := string(data)
	for _, requiredRule := range []string{"images[n].url", "absolute host path", "never show", "Never construct", "file://"} {
		if !strings.Contains(description, requiredRule) {
			t.Fatalf("image_generate description missing resource Markdown rule %q", requiredRule)
		}
	}
}

func defaultImageGenerateTestConfig() config.ImageGenerateConfig {
	return config.ImageGenerateConfig{
		Enabled:        true,
		DefaultProfile: "general",
		Profiles: map[string]config.ImageGenerateProfileConfig{
			"general": {
				ModelKey:        "image-model",
				Timeout:         120,
				Size:            "1024x1024",
				ResponseFormat:  "b64_json",
				OutputMimeType:  "image/png",
				MaxPromptChars:  4000,
				PersistArtifact: true,
			},
		},
	}
}

func imageGenerateTestExecutor(cfg config.ImageGenerateConfig, registry *models.ModelRegistry, chatsRoot string) *RuntimeToolExecutor {
	if len(cfg.Profiles) == 0 {
		cfg = defaultImageGenerateTestConfig()
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			ImageGenerate: cfg,
			Paths:         config.PathsConfig{ChatsDir: chatsRoot},
		},
		models: registry,
	}
	executor.httpClient = http.DefaultClient
	return executor
}

func writeImageGenerateRegistry(t *testing.T, baseURL string, withAPIKey bool) *models.ModelRegistry {
	return writeImageGenerateRegistryWithType(t, baseURL, withAPIKey, models.ModelTypeImageGeneration)
}

func writeImageGenerateRegistryWithType(t *testing.T, baseURL string, withAPIKey bool, modelType string) *models.ModelRegistry {
	return writeImageGenerateRegistryWithModel(t, baseURL, withAPIKey, modelType, []string{
		"  endpointPath: /v1/images/generations",
		"  timeout: 120",
		"  defaultSize: 1024x1024",
		"  responseFormats:",
		"    - b64_json",
		"    - url",
	})
}

func writeImageGenerateRegistryWithImageConfig(t *testing.T, baseURL string, withAPIKey bool, imageLines []string) *models.ModelRegistry {
	return writeImageGenerateRegistryWithModel(t, baseURL, withAPIKey, models.ModelTypeImageGeneration, imageLines)
}

func writeImageGenerateRegistryWithModel(t *testing.T, baseURL string, withAPIKey bool, modelType string, imageLines []string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatalf("mkdir providers: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	providerLines := []string{
		"key: test",
		"baseUrl: " + baseURL,
		"defaultModel: image-model",
		"protocols:",
		"  OPENAI:",
		"    endpointPath: /v1/chat/completions",
	}
	if withAPIKey {
		providerLines = append(providerLines[:2], append([]string{"apiKey: test-key"}, providerLines[2:]...)...)
	}
	if err := os.WriteFile(filepath.Join(root, "providers", "test.yml"), []byte(strings.Join(providerLines, "\n")), 0o644); err != nil {
		t.Fatalf("write provider: %v", err)
	}
	model := strings.Join([]string{
		"key: image-model",
		"provider: test",
		"type: " + modelType,
		"protocol: OPENAI",
		"modelId: image-model-id",
		"image:",
	}, "\n")
	if len(imageLines) > 0 {
		model += "\n" + strings.Join(imageLines, "\n")
	}
	if err := os.WriteFile(filepath.Join(root, "models", "image-model.yml"), []byte(model), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load model registry: %v", err)
	}
	return registry
}

func writeImageGenerateTestPNG(t *testing.T, path string, width int, height int, pixels []color.NRGBA) {
	t.Helper()
	if err := os.WriteFile(path, testPNGBytes(t, width, height, pixels), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testPNGBytes(t *testing.T, width int, height int, pixels []color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for index, pixel := range pixels {
		img.SetNRGBA(index%width, index/width, pixel)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
