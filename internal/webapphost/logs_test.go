package webapphost

import (
	"agent-platform/internal/config"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsBoundUnterminatedOutputAndRotate(t *testing.T) {
	root := t.TempDir()
	id := "webapp-0123456789abcdef"
	cfg := config.DefaultWebAppsConfig()
	cfg.Logs.MaxSizeMB = 1
	cfg.Logs.MaxFiles = 2
	dir := filepath.Join(root, id, "logs")
	w, err := newRotationWriter(dir, "stdout", cfg.Logs)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 80; i++ {
		if _, err := w.Write(bytes.Repeat([]byte("x"), 32768)); err != nil {
			t.Fatal(err)
		}
	}
	w.Write([]byte("tail-mark"))
	w.Close()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatal("rotation", len(files), err)
	}
	for _, file := range files {
		info, _ := file.Info()
		if info.Size() > 1<<20 {
			t.Fatal("log size exceeded")
		}
	}
	r := NewRunner(root, cfg, false)
	page, err := r.Logs(id, "stdout", "")
	if err != nil || len(page.Text) > 64<<10 || !strings.HasSuffix(page.Text, "tail-mark") || !page.Reset {
		t.Fatal("tail bounded", len(page.Text), err)
	}
	same, err := r.Logs(id, "stdout", page.Cursor)
	if err != nil || same.Text != "" || same.Reset {
		t.Fatal("unchanged cursor", same, err)
	}
	if _, err := r.Logs("../../etc", "stdout", ""); err == nil {
		t.Fatal("path accepted")
	}
}
