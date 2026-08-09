package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelRegistryKeepsPlaintextProviderAPIKey(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	_, provider, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if provider.APIKey != "plain-text" {
		t.Fatalf("expected plaintext apiKey, got %q", provider.APIKey)
	}
}

func TestLoadModelRegistryKeepsAESLikeProviderAPIKeyUnchanged(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: AES(v1:not-base64)")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	_, provider, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if provider.APIKey != "AES(v1:not-base64)" {
		t.Fatalf("expected AES-like apiKey to stay unchanged, got %q", provider.APIKey)
	}
}

func TestLoadModelRegistryParsesProviderMemoryEmbedding(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, strings.Join([]string{
		"apiKey: plain-text",
		"memory:",
		"  embedding:",
		"    model: text-embedding-3-small",
		"    dimension: 1536",
		"    timeout: 15",
	}, "\n"))

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	provider, err := registry.GetProvider("mock")
	if err != nil {
		t.Fatalf("GetProvider returned error: %v", err)
	}
	if provider.Memory.Embedding.Model != "text-embedding-3-small" {
		t.Fatalf("unexpected embedding model: %q", provider.Memory.Embedding.Model)
	}
	if provider.Memory.Embedding.Dimension != 1536 {
		t.Fatalf("unexpected embedding dimension: %d", provider.Memory.Embedding.Dimension)
	}
	if provider.Memory.Embedding.Timeout != 15 {
		t.Fatalf("unexpected embedding timeout: %d", provider.Memory.Embedding.Timeout)
	}
}

func TestLoadModelRegistryParsesProviderEmbedding(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, strings.Join([]string{
		"apiKey: plain-text",
		"embedding:",
		"  model: text-embedding-3-small",
		"  dimension: 1536",
		"  timeout: 15",
	}, "\n"))

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	provider, err := registry.GetProvider("mock")
	if err != nil {
		t.Fatalf("GetProvider returned error: %v", err)
	}
	if provider.Embedding.Model != "text-embedding-3-small" {
		t.Fatalf("unexpected embedding model: %q", provider.Embedding.Model)
	}
	if provider.Embedding.Dimension != 1536 {
		t.Fatalf("unexpected embedding dimension: %d", provider.Embedding.Dimension)
	}
	if provider.Embedding.Timeout != 15 {
		t.Fatalf("unexpected embedding timeout: %d", provider.Embedding.Timeout)
	}
	if provider.Memory.Embedding.Model != "" {
		t.Fatalf("provider.embedding must not populate memory embedding, got %#v", provider.Memory.Embedding)
	}
}

func TestLoadModelRegistryDefaultsModelVisionToFalse(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "name: Mock Model")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.IsVision {
		t.Fatal("expected model IsVision to default to false")
	}
	if model.Name != "Mock Model" {
		t.Fatalf("expected model name to parse, got %q", model.Name)
	}
}

func TestLoadModelRegistryParsesModelVisionTrue(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "isVision: true")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if !model.IsVision {
		t.Fatal("expected model IsVision to parse true")
	}
}

func TestLoadModelRegistryParsesModelVisionFalse(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "isVision: false")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.IsVision {
		t.Fatal("expected model IsVision to parse false")
	}
}

func TestLoadModelRegistryParsesMaxInputTokensAsContextWindow(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "maxInputTokens: 1048576")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.ContextWindow != 1048576 {
		t.Fatalf("expected maxInputTokens to populate ContextWindow, got %d", model.ContextWindow)
	}
}

func TestLoadModelRegistryParsesChatModelTimeout(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "timeout: 300")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.Timeout != 300 {
		t.Fatalf("expected chat model timeout to parse, got %d", model.Timeout)
	}
}

func TestLoadModelRegistryParsesModelPricing(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text",
		"pricing:",
		"  currency: CNY",
		"  unit: per_1m_tokens",
		"  inputCacheHit: 0.025",
		"  inputCacheMiss: 3.00",
		"  output: 6.00",
	)

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	if model.Pricing.Currency != "CNY" || model.Pricing.Unit != "per_1m_tokens" {
		t.Fatalf("unexpected model pricing metadata: %#v", model.Pricing)
	}
	if model.Pricing.InputCacheHit != 0.025 || model.Pricing.InputCacheMiss != 3 || model.Pricing.Output != 6 {
		t.Fatalf("unexpected model pricing values: %#v", model.Pricing)
	}
}

func TestLoadModelRegistryParsesModelIcon(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "icon: Mock Model Icon")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}
	model, err := registry.GetModel("mock-model")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if model.Icon != "Mock Model Icon" {
		t.Fatalf("model icon = %q, want Mock Model Icon", model.Icon)
	}
	for _, item := range registry.List() {
		if item.Key == "mock-model" && item.Icon == "Mock Model Icon" {
			return
		}
	}
	t.Fatalf("List did not preserve model icon: %#v", registry.List())
}

func TestLoadModelRegistryAllowsMissingModelIcon(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}
	model, err := registry.GetModel("mock-model")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if model.Icon != "" {
		t.Fatalf("model icon = %q, want empty", model.Icon)
	}
}

func TestLoadModelRegistryParsesTypedModels(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	modelsDir := filepath.Join(root, "models")
	if err := os.WriteFile(filepath.Join(modelsDir, "embedding.yml"), []byte(strings.Join([]string{
		"key: embedding-model",
		"provider: mock",
		"type: embedding",
		"modelId: text-embedding-v4",
		"embedding:",
		"  dimension: 1024",
		"  timeout: 60",
		"  endpointPath: /v1/embeddings",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write embedding model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "image.yml"), []byte(strings.Join([]string{
		"key: image-model",
		"provider: mock",
		"type: image-generation",
		"modelId: gpt-image-1",
		"image:",
		"  timeout: 120",
		"  defaultSize: 1024x1024",
		"  responseFormats:",
		"    - b64_json",
		"    - url",
		"  generation:",
		"    endpointPath: /v1/images/generations",
		"    requestFormat: openai-images-json",
		"  edit:",
		"    endpointPath: /v1/images/edits",
		"    requestFormat: openai-images-multipart",
		"    maskProtocol: openai-alpha",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write image model: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "vl.yml"), []byte(strings.Join([]string{
		"key: vl-model",
		"provider: mock",
		"type: vl",
		"modelId: qwen-vl-max",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write vl model: %v", err)
	}

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	chat, _, err := registry.Get("mock-model")
	if err != nil {
		t.Fatalf("Get chat model returned error: %v", err)
	}
	if chat.Type != ModelTypeChat {
		t.Fatalf("expected default chat type, got %#v", chat)
	}
	embedding, _, err := registry.GetEmbedding("embedding-model")
	if err != nil {
		t.Fatalf("GetEmbedding returned error: %v", err)
	}
	if embedding.Type != ModelTypeEmbedding ||
		embedding.ModelID != "text-embedding-v4" ||
		embedding.Embedding.Dimension != 1024 ||
		embedding.Embedding.Timeout != 60 ||
		embedding.Embedding.EndpointPath != "/v1/embeddings" {
		t.Fatalf("unexpected embedding model: %#v", embedding)
	}
	image, _, err := registry.GetImageGeneration("image-model")
	if err != nil {
		t.Fatalf("GetImageGeneration returned error: %v", err)
	}
	if image.Type != ModelTypeImageGeneration ||
		image.Image.Generation.EndpointPath != "/v1/images/generations" ||
		image.Image.Generation.RequestFormat != ImageGenerationRequestFormatOpenAIImagesJSON ||
		image.Image.Timeout != 120 ||
		image.Image.DefaultSize != "1024x1024" ||
		len(image.Image.ResponseFormats) != 2 ||
		image.Image.ResponseFormats[1] != "url" ||
		image.Image.Edit.EndpointPath != "/v1/images/edits" ||
		image.Image.Edit.RequestFormat != ImageEditRequestFormatOpenAIImagesMultipart ||
		image.Image.Edit.MaskProtocol != ImageMaskProtocolOpenAIAlpha {
		t.Fatalf("unexpected image model: %#v", image)
	}
	vl, _, err := registry.GetVL("vl-model")
	if err != nil {
		t.Fatalf("GetVL returned error: %v", err)
	}
	if vl.Type != ModelTypeVL || vl.ModelID != "qwen-vl-max" {
		t.Fatalf("unexpected vl model: %#v", vl)
	}
	if _, _, err := registry.Get("embedding-model"); err == nil || !strings.Contains(err.Error(), "want chat") {
		t.Fatalf("expected chat Get to reject embedding model, got %v", err)
	}
	if _, _, err := registry.Get("vl-model"); err == nil || !strings.Contains(err.Error(), "want chat") {
		t.Fatalf("expected chat Get to reject vl model, got %v", err)
	}
	defaultModel, _, err := registry.Default()
	if err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if defaultModel.Key != "mock-model" {
		t.Fatalf("expected default to skip non-chat models, got %#v", defaultModel)
	}
}

func TestLoadModelRegistryRejectsInvalidModelType(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", "type: audio")

	_, err := LoadModelRegistry(root)
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("expected invalid type error, got %v", err)
	}
}

func TestLoadModelRegistryRejectsInvalidImageEditConfig(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	content := strings.Join([]string{
		"key: bad-image",
		"provider: mock",
		"type: image-generation",
		"modelId: bad-image",
		"image:",
		"  generation:",
		"    endpointPath: /v1/images/generations",
		"    requestFormat: openai-images-json",
		"  edit:",
		"    endpointPath: /v1/chat/completions",
		"    requestFormat: openai-chat-completions",
		"    maskProtocol: openai-alpha",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "models", "bad-image.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), "openai-alpha requires requestFormat openai-images-multipart") {
		t.Fatalf("expected invalid image edit config, got %v", err)
	}
}

func TestValidateModelImageConfigAcceptsSupportedRequestFormats(t *testing.T) {
	for _, generationFormat := range []string{
		ImageGenerationRequestFormatOpenAIImagesJSON,
		ImageGenerationRequestFormatOpenAIChatCompletions,
	} {
		for _, edit := range []ModelImageEditConfig{
			{},
			{Configured: true, EndpointPath: "/v1/images/edits", RequestFormat: ImageEditRequestFormatOpenAIImagesMultipart, MaskProtocol: ImageMaskProtocolNone},
			{Configured: true, EndpointPath: "/v1/images/edits", RequestFormat: ImageEditRequestFormatOpenAIImagesMultipart, MaskProtocol: ImageMaskProtocolOpenAIAlpha},
			{Configured: true, EndpointPath: "/v1/chat/completions", RequestFormat: ImageEditRequestFormatOpenAIChatCompletions, MaskProtocol: ImageMaskProtocolNone},
		} {
			image := ModelImageConfig{
				Generation: ModelImageGenerationConfig{EndpointPath: "/configured", RequestFormat: generationFormat},
				Edit:       edit,
			}
			if err := ValidateModelImageConfig(image); err != nil {
				t.Fatalf("generation=%q edit=%#v: %v", generationFormat, edit, err)
			}
		}
	}
}

func TestValidateModelImageConfigRejectsMissingAndUnknownFormats(t *testing.T) {
	tests := []struct {
		name  string
		image ModelImageConfig
		want  string
	}{
		{name: "missing generation", image: ModelImageConfig{}, want: "generation: endpointPath is required"},
		{name: "unknown generation", image: ModelImageConfig{Generation: ModelImageGenerationConfig{EndpointPath: "/generate", RequestFormat: "unknown"}}, want: "generation: requestFormat"},
		{name: "missing edit endpoint", image: ModelImageConfig{Generation: ModelImageGenerationConfig{EndpointPath: "/generate", RequestFormat: ImageGenerationRequestFormatOpenAIImagesJSON}, Edit: ModelImageEditConfig{Configured: true, RequestFormat: ImageEditRequestFormatOpenAIImagesMultipart}}, want: "edit: endpointPath is required"},
		{name: "unknown edit", image: ModelImageConfig{Generation: ModelImageGenerationConfig{EndpointPath: "/generate", RequestFormat: ImageGenerationRequestFormatOpenAIImagesJSON}, Edit: ModelImageEditConfig{Configured: true, EndpointPath: "/edit", RequestFormat: "unknown"}}, want: "edit: requestFormat"},
		{name: "chat alpha", image: ModelImageConfig{Generation: ModelImageGenerationConfig{EndpointPath: "/generate", RequestFormat: ImageGenerationRequestFormatOpenAIImagesJSON}, Edit: ModelImageEditConfig{Configured: true, EndpointPath: "/chat", RequestFormat: ImageEditRequestFormatOpenAIChatCompletions, MaskProtocol: ImageMaskProtocolOpenAIAlpha}}, want: "requires requestFormat openai-images-multipart"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateModelImageConfig(test.image); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadModelRegistryRejectsLegacyImageEndpointAndMissingGeneration(t *testing.T) {
	for _, test := range []struct {
		name       string
		imageLines []string
		want       string
	}{
		{name: "legacy endpoint", imageLines: []string{"  endpointPath: /v1/images/generations"}, want: "image.endpointPath is no longer supported"},
		{name: "missing generation", imageLines: []string{"  timeout: 120"}, want: "image: generation: endpointPath is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestProviderAndModel(t, root, "apiKey: plain-text")
			content := strings.Join(append([]string{
				"key: invalid-image",
				"provider: mock",
				"type: image-generation",
				"modelId: invalid-image",
				"image:",
			}, test.imageLines...), "\n")
			if err := os.WriteFile(filepath.Join(root, "models", "invalid-image.yml"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadModelRegistry(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadModelRegistryRejectsNonChatACPPassthroughModel(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "vl-acp", "qwen-vl-max", strings.Join([]string{
		"type: vl",
		"protocol: ACP_PASSTHROUGH",
	}, "\n"))

	_, err := LoadModelRegistry(root)
	if err == nil || !strings.Contains(err.Error(), "ACP_PASSTHROUGH is only supported for type: chat") {
		t.Fatalf("expected non-chat ACP model rejection, got %v", err)
	}
}

func TestProviderlessModelCanBeListedAndReadWithoutProvider(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "gpt-5-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, err := registry.GetModel("gpt-5-codex")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if model.Key != "gpt-5-codex" || model.ModelID != "gpt-5-codex" || model.Provider != "" {
		t.Fatalf("unexpected providerless model %#v", model)
	}

	found := false
	for _, item := range registry.List() {
		if item.Key == "gpt-5-codex" && item.ModelID == "gpt-5-codex" && item.Provider == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected providerless model in List")
	}
}

func TestProviderlessModelStillFailsProviderBackedGet(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "gpt-5-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	if _, _, err := registry.Get("gpt-5-codex"); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider-backed Get to fail, got %v", err)
	}
}

func TestACPPassthroughModelCanBeReadWithoutProvider(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "gpt-5-codex", "gpt-5-codex", "protocol: ACP_PASSTHROUGH")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, err := registry.GetModel("gpt-5-codex")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if !IsACPPassthroughModel(model) {
		t.Fatalf("expected ACP passthrough model, got %#v", model)
	}
	if _, _, err := registry.Get("gpt-5-codex"); err == nil || !strings.Contains(err.Error(), "ACP_PASSTHROUGH") {
		t.Fatalf("expected native provider Get to reject ACP passthrough model, got %v", err)
	}
}

func TestDefaultSkipsProviderlessModels(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text")
	writeTestProviderlessModel(t, root, "aaa-codex", "gpt-5-codex")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	model, provider, err := registry.Default()
	if err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if model.Key != "mock-model" || provider.Key != "mock" {
		t.Fatalf("expected provider-backed default, got model=%#v provider=%#v", model, provider)
	}
}

func TestDefaultSkipsProviderModelsWithEmptyAPIKey(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey:")

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}

	if _, _, err := registry.Default(); err == nil || !strings.Contains(err.Error(), "no provider-backed models") {
		t.Fatalf("expected no provider-backed models default error, got %v", err)
	}
}

func TestLoadModelRegistryParsesAndClonesReasoningEffortMapping(t *testing.T) {
	root := t.TempDir()
	writeTestProviderAndModel(t, root, "apiKey: plain-text", strings.Join([]string{
		"type: chat",
		"reasoningEffortMapping:",
		"  LOW: LOW",
		"  MEDIUM: HIGH",
		"  HIGH: HIGH",
		"  XHIGH: HIGH",
		"  MAX: MAX",
	}, "\n"))

	registry, err := LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("LoadModelRegistry returned error: %v", err)
	}
	model, err := registry.GetModel("mock-model")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if model.ReasoningEffortMapping[ReasoningEffortMedium] != ReasoningEffortHigh || model.ReasoningEffortMapping[ReasoningEffortXHigh] != ReasoningEffortHigh {
		t.Fatalf("unexpected mapping %#v", model.ReasoningEffortMapping)
	}
	model.ReasoningEffortMapping[ReasoningEffortLow] = ReasoningEffortMax
	again, err := registry.GetModel("mock-model")
	if err != nil {
		t.Fatalf("GetModel returned error: %v", err)
	}
	if again.ReasoningEffortMapping[ReasoningEffortLow] != ReasoningEffortLow {
		t.Fatalf("registry mapping was mutated through clone: %#v", again.ReasoningEffortMapping)
	}
}

func TestLoadModelRegistryRejectsInvalidReasoningEffortMapping(t *testing.T) {
	tests := []struct {
		name       string
		modelLines []string
		want       string
	}{
		{
			name: "missing enabled effort",
			modelLines: []string{
				"reasoningEffortMapping:", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: HIGH",
			},
			want: "must define MAX",
		},
		{
			name: "none key",
			modelLines: []string{
				"reasoningEffortMapping:", "  NONE: LOW", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: HIGH", "  MAX: MAX",
			},
			want: `key "NONE"`,
		},
		{
			name: "invalid value",
			modelLines: []string{
				"reasoningEffortMapping:", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: EXTRA_HIGH", "  MAX: MAX",
			},
			want: "reasoningEffortMapping.XHIGH",
		},
		{
			name: "anthropic protocol",
			modelLines: []string{
				"protocol: ANTHROPIC", "reasoningEffortMapping:", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: HIGH", "  MAX: MAX",
			},
			want: "native OPENAI chat models",
		},
		{
			name: "acp passthrough protocol",
			modelLines: []string{
				"protocol: ACP_PASSTHROUGH", "reasoningEffortMapping:", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: HIGH", "  MAX: MAX",
			},
			want: "native OPENAI chat models",
		},
		{
			name: "non chat model",
			modelLines: []string{
				"type: embedding", "embedding:", "  dimension: 8", "reasoningEffortMapping:", "  LOW: LOW", "  MEDIUM: HIGH", "  HIGH: HIGH", "  XHIGH: HIGH", "  MAX: MAX",
			},
			want: "only supported for type: chat",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestProviderAndModel(t, root, "apiKey: plain-text", tc.modelLines...)
			_, err := LoadModelRegistry(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func writeTestProviderAndModel(t *testing.T, root string, apiKeyLine string, modelLines ...string) {
	t.Helper()

	providersDir := filepath.Join(root, "providers")
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatalf("mkdir providers dir: %v", err)
	}
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}

	providerConfig := strings.Join([]string{
		"key: mock",
		"baseUrl: https://example.com",
		apiKeyLine,
		"defaultModel: mock-model",
	}, "\n")
	if err := os.WriteFile(filepath.Join(providersDir, "mock.yml"), []byte(providerConfig), 0o644); err != nil {
		t.Fatalf("write provider config: %v", err)
	}

	modelConfig := strings.Join([]string{
		"key: mock-model",
		"provider: mock",
		"protocol: OPENAI",
		"modelId: mock-model-id",
	}, "\n")
	if len(modelLines) > 0 {
		modelConfig += "\n" + strings.Join(modelLines, "\n")
	}
	if err := os.WriteFile(filepath.Join(modelsDir, "mock-model.yml"), []byte(modelConfig), 0o644); err != nil {
		t.Fatalf("write model config: %v", err)
	}
}

func writeTestProviderlessModel(t *testing.T, root string, key string, modelID string, extraLines ...string) {
	t.Helper()
	modelsDir := filepath.Join(root, "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models dir: %v", err)
	}
	modelConfig := strings.Join([]string{
		"key: " + key,
		"name: Providerless Model",
		"modelId: " + modelID,
	}, "\n")
	if len(extraLines) > 0 {
		modelConfig += "\n" + strings.Join(extraLines, "\n")
	}
	if err := os.WriteFile(filepath.Join(modelsDir, key+".yml"), []byte(modelConfig), 0o644); err != nil {
		t.Fatalf("write providerless model config: %v", err)
	}
}
