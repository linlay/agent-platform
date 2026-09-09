package chatresource

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/multimodal"
)

func TestPrepareSteerImagesUsesCurrentChatAndFreezesBytes(t *testing.T) {
	root := t.TempDir()
	chatDir := filepath.Join(root, "chat-a")
	if err := os.Mkdir(chatDir, 0700); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(chatDir, "test image.png")
	if err := os.WriteFile(file, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, container := range []bool{false, true} {
		refs, blocks, err := PrepareSteerImages("chat-a", chatDir, container, []api.Reference{{URL: "test%20image.png", Path: "/untrusted/path", MimeType: "text/plain", Name: "fake.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(file)
		if err != nil {
			t.Fatal(err)
		}
		if container {
			want = "/chat/test image.png"
		}
		if len(refs) != 1 || refs[0].Path != want || refs[0].MimeType != "image/png" || refs[0].Name != "test image.png" || len(blocks) != 1 {
			t.Fatalf("refs=%#v blocks=%#v", refs, blocks)
		}
	}
	if err := os.Symlink(filepath.Dir(chatDir), filepath.Join(chatDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chatDir, "text.png"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(chatDir, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(multimodal.DefaultMaxImageBytes + 1)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"../other.png", "%2e%2e/other.png", "escape/other.png", "https://example.com/a.png", "/absolute.png", "test%20image.png?x=1", "text.png", "large.png", "missing.png"} {
		t.Run(url, func(t *testing.T) {
			refs, blocks, err := PrepareSteerImages("chat-a", chatDir, false, []api.Reference{{URL: "test%20image.png"}, {URL: url}})
			if err == nil || refs != nil || blocks != nil {
				t.Fatalf("must reject the complete input: %v %#v", err, refs)
			}
		})
	}
	_, blocks, err := PrepareSteerImages("chat-a", chatDir, false, []api.Reference{{URL: "test%20image.png"}})
	if err != nil {
		t.Fatal(err)
	}
	before := blocks[0]["image_url"].(map[string]any)["url"].(string)
	if err := os.WriteFile(file, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(before, "data:image/png;base64,") || blocks[0]["image_url"].(map[string]any)["url"] != before {
		t.Fatal("queued image changed")
	}
}
