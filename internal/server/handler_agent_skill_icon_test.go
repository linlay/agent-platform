package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func writeAgentSkillIconPNG(t *testing.T, root, key string, shade uint8) {
	t.Helper()
	dir := filepath.Join(root, key, "assets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: shade, A: 255})
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".png"), data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSkillIconUsesRuntimeOrCenterWithoutMixingPrivateSkills(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	response := getAPIData[api.AgentSkillsResponse](t, f.server, http.MethodGet, "/api/skills?agentKey=mock-agent", nil)
	def, _ := f.registry.AgentDefinition("mock-agent")
	for _, skill := range response.Skills {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest("GET", skill.Icon, nil))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("icon %s: %d %s", skill.Key, rec.Code, rec.Body.String())
		}
		root := f.cfg.Paths.SkillsCenterDir
		if skill.AgentHasSkill {
			root = filepath.Join(def.RuntimeDir, "skills")
		}
		expected, err := os.ReadFile(filepath.Join(root, skill.Key, "assets", skill.Key+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rec.Body.Bytes(), expected) {
			t.Fatalf("wrong source for %s", skill.Key)
		}
		request := httptest.NewRequest("GET", skill.Icon, nil)
		request.Header.Set("If-None-Match", rec.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		f.server.ServeHTTP(cached, request)
		if cached.Code != http.StatusNotModified {
			t.Fatalf("cache status: %d", cached.Code)
		}
	}
	// A same-named center skill must never supply a missing private icon.
	privateIcon := filepath.Join(def.RuntimeDir, "skills", "private-skill", "assets", "private-skill.png")
	if err := os.Remove(privateIcon); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/skills/icon?agentKey=mock-agent&key=private-skill", nil))
	if rec.Code != 404 {
		t.Fatalf("private icon fallback status %d", rec.Code)
	}
	outside := filepath.Join(f.cfg.Paths.SkillsCenterDir, "private-skill", "assets", "private-skill.png")
	if err := os.Symlink(outside, privateIcon); err == nil {
		rec = httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/skills/icon?agentKey=mock-agent&key=private-skill", nil))
		if rec.Code != 404 {
			t.Fatalf("symlink icon status %d", rec.Code)
		}
	}
}

func TestAgentSkillIconMissingAndInvalidInputs(t *testing.T) {
	f := newTestFixture(t)
	def, _ := f.registry.AgentDefinition("mock-agent")
	if err := os.Remove(filepath.Join(def.RuntimeDir, "skills", "mock-skill", "assets", "mock-skill.png")); err != nil {
		t.Fatal(err)
	}
	response := getAPIData[api.AgentSkillsResponse](t, f.server, http.MethodGet, "/api/skills?agentKey=mock-agent", nil)
	for _, skill := range response.Skills {
		if skill.Icon != "" {
			t.Fatalf("unexpected icon: %s", skill.Icon)
		}
	}
	for _, item := range []struct {
		query  string
		status int
	}{
		{"key=mock-skill", 400}, {"agentKey=mock-agent&key=../secret", 400},
		{"agentKey=missing&key=mock-skill", 404}, {"agentKey=mock-agent&key=missing", 404},
		{"agentKey=mock-agent&key=mock-skill", 404},
	} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest("GET", "/api/skills/icon?"+item.query, nil))
		if rec.Code != item.status {
			t.Fatalf("%s: %d", item.query, rec.Code)
		}
		if strings.Contains(rec.Body.String(), f.cfg.Paths.AgentsDir) {
			t.Fatal("filesystem path leaked")
		}
	}
}
