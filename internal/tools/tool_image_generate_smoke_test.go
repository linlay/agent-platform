package tools

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestImageGenerateBabelArkSmoke(t *testing.T) {
	if os.Getenv("AP_RUN_BABELARK_IMAGE_SMOKE") != "1" {
		t.Skip("set AP_RUN_BABELARK_IMAGE_SMOKE=1 to run paid BabelArk image smoke tests")
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
	executor, err := NewRuntimeToolExecutor(cfg, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor.WithModelRegistry(registry)

	chatID := "babelark-image-smoke"
	chatDir := filepath.Join(chatsRoot, chatID)
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(chatDir, "target.png")
	if err := os.WriteFile(targetPath, babelArkSmokeTargetPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	profiles := []string{
		"babelark-gpt-image-2",
		"babelark-gemini-3_1-flash-image",
		"babelark-gemini-3_1-flash-lite-image",
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile, func(t *testing.T) {
			for _, operation := range []string{"generation", "edit"} {
				operation := operation
				t.Run(operation, func(t *testing.T) {
					args := map[string]any{
						"profile": profile,
						"prompt":  "Create a simple flat icon: one centered blue square on a plain white background, no text.",
						"n":       1,
					}
					if operation == "edit" {
						args["prompt"] = "Keep the simple composition and change the centered blue square to green. Do not add text."
						args["images"] = []any{map[string]any{
							"source_type": "reference_name",
							"value":       "target.png",
						}}
					}
					runID := "smoke-" + profile + "-" + operation
					result, err := executor.invokeImageGenerate(context.Background(), args, &contracts.ExecutionContext{Session: contracts.QuerySession{
						ChatID: chatID,
						RunID:  runID,
						RuntimeContext: contracts.RuntimeRequestContext{LocalPaths: contracts.LocalPaths{
							ChatDir: chatDir,
						}},
					}})
					if err != nil {
						t.Fatal(err)
					}
					if result.Error != "" {
						t.Fatalf("provider smoke failed: %#v", result.Structured)
					}
					if result.Structured["operation"] != operation {
						t.Fatalf("operation=%#v want=%q", result.Structured["operation"], operation)
					}
					images, ok := result.Structured["images"].([]map[string]any)
					if !ok || len(images) == 0 {
						t.Fatalf("missing materialized images: %#v", result.Structured)
					}
					path := contracts.AnyStringNode(images[0]["path"])
					url := contracts.AnyStringNode(images[0]["url"])
					if path == "" || url == "" || filepath.Dir(path) != chatDir {
						t.Fatalf("invalid materialized image: %#v", images[0])
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("stat materialized image: %v", err)
					}
					t.Logf("profile=%s operation=%s url=%s", profile, operation, url)
				})
			}
		})
	}
}

func babelArkSmokeTargetPNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for index := range canvas.Pix {
		canvas.Pix[index] = 255
	}
	for y := 320; y < 704; y++ {
		for x := 320; x < 704; x++ {
			canvas.SetNRGBA(x, y, color.NRGBA{B: 220, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
