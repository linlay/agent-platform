package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

// TestSpeechGenerateProviderSmoke calls the real providers configured in
// configs/tools.yml > speech-generate and the active registry.
func TestSpeechGenerateProviderSmoke(t *testing.T) {
	if os.Getenv("AP_RUN_SPEECH_SMOKE") != "1" {
		t.Skip("set AP_RUN_SPEECH_SMOKE=1 to run paid speech smoke tests")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.LoadOptions{ConfigDir: repositoryRoot})
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}
	registry, err := models.LoadModelRegistry(cfg.Paths.RegistriesDir)
	if err != nil {
		t.Fatalf("load active model registry: %v", err)
	}
	chatsRoot := t.TempDir()
	cfg.Paths.ChatsDir = chatsRoot
	executor, err := NewRuntimeToolExecutor(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor.WithModelRegistry(registry)

	profiles := strings.Fields(strings.ReplaceAll(os.Getenv("AP_SPEECH_SMOKE_PROFILES"), ",", " "))
	if len(profiles) == 0 {
		for name := range cfg.SpeechGenerate.Profiles {
			profiles = append(profiles, name)
		}
	}
	for _, profile := range profiles {
		t.Run(profile, func(t *testing.T) {
			execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "speech-smoke", RunID: "run-" + profile}}
			result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{
				"profile": profile,
				"text":    "你好，这是一段语音合成测试。",
			}, execCtx)
			if err != nil || result.Error != "" {
				t.Fatalf("speech_generate: err=%v output=%s", err, result.Output)
			}
			audio := contracts.AnyMapNode(result.Structured["audio"])
			t.Logf("profile=%s url=%v format=%v bytes=%v usage=%+v", profile, audio["url"], audio["format"], audio["sizeBytes"], result.ModelUsage)
			if len(result.ModelUsage) != 1 || result.ModelUsage[0].Characters <= 0 {
				t.Fatalf("provider did not report billed characters: %+v", result.ModelUsage)
			}
		})
	}
}
