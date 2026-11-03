package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/runtimeskills"
)

func TestAppMailboxOptIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings string
		existing bool
		enabled  bool
	}{
		{name: "default"},
		{name: "disabled", settings: "mail:\n  enabled: false\n"},
		{name: "disabled preserves existing database", settings: "mail:\n  enabled: false\n", existing: true},
		{name: "enabled", settings: "mail:\n  enabled: true\n", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Cleanup(func() {
				if err := runtimeskills.Remove(root); err != nil {
					t.Error(err)
				}
			})
			for _, key := range []string{"AP_RUNTIME_REGISTRIES_DIR", "AP_RUNTIME_CHATS_DIR", "AP_RUNTIME_MEMORY_DIR", "AP_RUNTIME_PAN_DIR", "AP_RUNTIME_STATE_DIR"} {
				t.Setenv(key, "")
			}
			runtimeRoot := filepath.Join(root, "runtime")
			t.Setenv("AP_RUNTIME_DIR", runtimeRoot)
			if err := os.MkdirAll(filepath.Join(root, "configs"), 0700); err != nil {
				t.Fatal(err)
			}
			settings := "auth:\n  enabled: false\ncontainer-hub:\n  enabled: false\nautomation:\n  enabled: false\n" + tc.settings
			if err := os.WriteFile(filepath.Join(root, "configs", "runtime.yml"), []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "configs", "tools.yml"), []byte("bash:\n  git-bash:\n    enabled: false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			mailPath := filepath.Join(runtimeRoot, "chats", "mail.db")
			const oldDatabase = "unreleased mailbox database"
			if tc.existing {
				store, err := chat.NewFileStoreAtStartup(filepath.Dir(mailPath))
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mailPath, []byte(oldDatabase), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			application, err := New(ctx, config.LoadOptions{ConfigDir: root})
			if err != nil {
				t.Fatal(err)
			}
			defer application.Close()
			if (application.mailbox != nil) != tc.enabled {
				t.Fatalf("mailbox enabled = %v, want %v", application.mailbox != nil, tc.enabled)
			}
			for _, req := range []*http.Request{
				httptest.NewRequest(http.MethodGet, "/healthz", nil),
				httptest.NewRequest(http.MethodGet, "/api/mails", nil),
				httptest.NewRequest(http.MethodPost, "/api/mails/action", strings.NewReader(`{}`)),
			} {
				recorder := httptest.NewRecorder()
				application.Router.ServeHTTP(recorder, req)
				want := http.StatusServiceUnavailable
				if tc.enabled || req.URL.Path == "/healthz" {
					want = http.StatusOK
					if req.Method == http.MethodPost {
						want = http.StatusNotFound
					}
				}
				if recorder.Code != want {
					t.Fatalf("%s %s: %d %s", req.Method, req.URL, recorder.Code, recorder.Body.String())
				}
			}
			if !tc.enabled && !tc.existing {
				if matches, err := filepath.Glob(mailPath + "*"); err != nil || len(matches) != 0 {
					t.Fatalf("disabled mailbox created database files: %v %v", matches, err)
				}
			} else if tc.existing {
				if data, err := os.ReadFile(mailPath); err != nil || string(data) != oldDatabase {
					t.Fatalf("disabled mailbox changed existing database: %q %v", data, err)
				}
			} else if _, err := os.Stat(mailPath); err != nil {
				t.Fatalf("enabled mailbox did not create database: %v", err)
			}
		})
	}
}
