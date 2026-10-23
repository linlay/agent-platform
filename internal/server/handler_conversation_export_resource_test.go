package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/config"
)

func TestChatExportResourceAuthorityAndFailureReasons(t *testing.T) {
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const id = "export-media"
	if _, _, err := store.EnsureChat(id, "agent", "media"); err != nil {
		t.Fatal(err)
	}
	root := store.ChatDir(id)
	if err := os.MkdirAll(filepath.Join(root, "artifacts/run"), 0700); err != nil {
		t.Fatal(err)
	}
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	for name, body := range map[string][]byte{"image.svg": image, "notes.txt": []byte("text"), "artifacts/run/report.txt": []byte("changed")} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte("initial"))
	if err := store.AppendArtifactManifest(id, "run", testEpochMillis, []map[string]any{{"type": "file", "artifactId": "report", "url": "artifacts/run/report.txt", "name": "report.txt", "mimeType": "text/plain", "sizeBytes": 7, "sha256": hex.EncodeToString(hash[:])}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{Chats: store}, chatResources: chatresource.NewService(store)}
	s.deps.Config.RuntimeMode = config.RuntimeModeDesktop
	app := &Principal{Subject: "desktop", Claims: map[string]any{"scope": "app", "deviceId": "device"}}
	request := func(body string, p *Principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/chat/export/resources/read", strings.NewReader(body))
		if p != nil {
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		w := httptest.NewRecorder()
		s.handleChatExportResource(w, r)
		return w
	}
	for _, tc := range []struct {
		ref    string
		status int
		reason string
	}{{"image.svg", 200, ""}, {"notes.txt", 415, "unsupported"}, {"missing.png", 404, "not_found"}, {"artifacts/run/report.txt", 409, "changed"}, {"../other/image.svg", 400, "invalid"}, {".tools/secret", 400, "invalid"}} {
		w := request(`{"chatId":"`+id+`","sourceRef":"`+tc.ref+`"}`, app)
		if w.Code != tc.status || tc.reason != "" && !strings.Contains(w.Body.String(), tc.reason) {
			t.Fatalf("%s status=%d body=%s", tc.ref, w.Code, w.Body.String())
		}
		if tc.status == 200 && (w.Header().Get("Content-Type") != "image/svg+xml" || w.Body.String() != string(image)) {
			t.Fatalf("media response %v %s", w.Header(), w.Body.String())
		}
	}
	for _, p := range []*Principal{nil, {Subject: "user", Claims: map[string]any{"scope": "user", "deviceId": "forged"}}, {Subject: "app", Claims: map[string]any{"scope": "app"}}} {
		if w := request(`{"chatId":"`+id+`","sourceRef":"image.svg"}`, p); w.Code != 401 && w.Code != 403 {
			t.Fatalf("invalid principal accepted: %d", w.Code)
		}
	}
	if w := request(`{"chatId":"`+id+`","sourceRef":"image.svg","deviceId":"forged"}`, app); w.Code != 400 {
		t.Fatalf("unknown request identity accepted: %d", w.Code)
	}
	s.deps.Config.RuntimeMode = config.RuntimeModeStandalone
	if w := request(`{"chatId":"`+id+`","sourceRef":"image.svg"}`, app); w.Code != 403 {
		t.Fatalf("standalone accepted: %d", w.Code)
	}

	// Exercise the registered route and real JWT middleware, including when
	// global auth is disabled. Handler-only principal injection misses this case.
	privateKey, publicKeyFile := writeTestJWTKeyPair(t, t.TempDir())
	s.deps.Config.RuntimeMode = config.RuntimeModeDesktop
	s.deps.Config.Auth.LocalPublicKeyFile = publicKeyFile
	s.authVerifier = NewJWTVerifier(s.deps.Config.Auth)
	s.router = http.NewServeMux()
	s.routes()
	for _, authEnabled := range []bool{false, true} {
		s.deps.Config.Auth.Enabled = authEnabled
		for _, tc := range []struct {
			token  string
			status int
		}{
			{"", 401},
			{"invalid", 401},
			{mustSignRS256JWT(t, privateKey, map[string]any{"sub": "desktop", "scope": "app", "deviceId": "device"}), 200},
			{mustSignRS256JWT(t, privateKey, map[string]any{"sub": "user", "scope": "user", "deviceId": "device"}), 403},
			{mustSignRS256JWT(t, privateKey, map[string]any{"sub": "desktop", "scope": "app"}), 403},
		} {
			r := httptest.NewRequest(http.MethodPost, "/api/chat/export/resources/read", strings.NewReader(`{"chatId":"`+id+`","sourceRef":"image.svg"}`))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status || tc.status == 200 && w.Body.String() != string(image) {
				t.Fatalf("authEnabled=%v: status=%d want=%d body=%s", authEnabled, w.Code, tc.status, w.Body.String())
			}
		}
	}
}
