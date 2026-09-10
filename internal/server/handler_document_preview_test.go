package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/documentpreview"
)

func TestDocumentPreviewDefaultsOffAndUsesHTTP(t *testing.T) {
	f := newTestFixture(t)
	r := httptest.NewRecorder()
	f.server.ServeHTTP(r, httptest.NewRequest("GET", "/api/document/preview/capabilities", nil))
	if r.Code != 200 {
		t.Fatalf("status=%d", r.Code)
	}
	var envelope api.ApiResponse[documentpreview.Capabilities]
	if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Enabled || envelope.Data.MaxFileBytes != 50<<20 {
		t.Fatalf("capabilities=%+v", envelope.Data)
	}
	r = httptest.NewRecorder()
	f.server.ServeHTTP(r, httptest.NewRequest("POST", "/api/document/preview", nil))
	if r.Code != 503 {
		t.Fatalf("disabled status=%d", r.Code)
	}
}

func TestPreviewResourceResolverPreservesChatReadPermissions(t *testing.T) {
	f := newTestFixture(t)
	for _, tc := range []struct{ id, agent, team string }{{"preview-owned", "mock-agent", ""}, {"preview-team", "", "team-1"}} {
		if _, _, err := f.chats.EnsureChatWithSource(tc.id, tc.agent, tc.team, "preview", api.ChatSourceQueryPrefix+"alice"); err != nil {
			t.Fatal(err)
		}
		dir := f.chats.ChatDir(tc.id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "报告%.docx"), []byte("source"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"alice", "bob"} {
			req := httptest.NewRequest(http.MethodPost, "/api/document/preview", nil)
			req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: user}))
			_, err := f.server.resolvePreviewSource(req, documentpreview.Source{Kind: "chat-resource", ChatID: tc.id, RelativePath: "报告%.docx"})
			if (err == nil) != (user == "alice") {
				t.Fatalf("%s %s access error=%v", tc.id, user, err)
			}
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/document/preview", nil)
	for _, path := range []string{"../escape.docx", "%2e%2e/escape.docx", ".tools/private.docx", ".btw/private.docx", "/etc/secret.docx"} {
		_, err := f.server.resolvePreviewSource(req, documentpreview.Source{Kind: "chat-resource", ChatID: "preview-owned", RelativePath: path})
		if err == nil {
			t.Fatalf("unsafe source allowed: %s", path)
		}
	}
}

func TestPreviewWorkspaceResolverRejectsSymlinkEscape(t *testing.T) {
	f, root, _ := newAgentFileTestFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.docx")
	_ = os.WriteFile(outside, []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "escape.docx")); err != nil {
		t.Skip("symlinks unavailable")
	}
	req := httptest.NewRequest("POST", "/api/document/preview", nil)
	_, err := f.server.resolvePreviewSource(req, documentpreview.Source{Kind: "workspace-file", AgentKey: "coder-file", Path: "escape.docx"})
	var e *documentpreview.Error
	if !errors.As(err, &e) || e.Status != 403 {
		t.Fatalf("escape result=%v", err)
	}
	if _, err := f.server.resolvePreviewSource(req, documentpreview.Source{Kind: "workspace-file", AgentKey: "coder-file", Path: "docs/hello.md"}); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewArchivedResourceRetainsOwnerAuthorization(t *testing.T) {
	f := newTestFixture(t)
	const id = "preview-archive"
	if _, _, err := f.chats.EnsureChatWithSource(id, "mock-agent", "", "preview", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	startServerFixtureRun(t, f.chats, id, "preview-archive-run", 1700000000000)
	if err := f.chats.OnRunCompleted(chat.RunCompletion{ChatID: id, RunID: "preview-archive-run", AgentKey: "mock-agent", InitialMessage: "preview", AssistantText: "done", FinishReason: "complete", StartedAtMillis: 1700000000000, UpdatedAtMillis: 1700000000250}); err != nil {
		t.Fatal(err)
	}

	dir := f.chats.ChatDir(id)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts", "report.xlsx"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	archives, err := chat.NewArchiveStoreAtStartup(f.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatal(err)
	}
	f.server.deps.Archives = archives
	if err := chat.NewArchiver(f.chats.(*chat.FileStore), archives).ArchiveChat(id); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "bob"} {
		req := httptest.NewRequest("POST", "/api/document/preview", nil)
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: user}))
		resolved, err := f.server.resolvePreviewSource(req, documentpreview.Source{Kind: "chat-resource", ChatID: id, RelativePath: "artifacts/report.xlsx"})
		if (err == nil) != (user == "alice") {
			t.Fatalf("%s access=%v", user, err)
		}
		expected, _ := filepath.EvalSymlinks(filepath.Join(archives.ChatDir(id), "artifacts", "report.xlsx"))
		if user == "alice" && resolved.Path != expected {
			t.Fatalf("wrong archived path: %s", resolved.Path)
		}
	}
}
