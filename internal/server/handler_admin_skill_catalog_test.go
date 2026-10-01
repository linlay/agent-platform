package server

import (
	"agent-platform/internal/api"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdminSkillCatalogAndSharedPins(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "mock-skill")
	request := func(user, method, path, body string, status int) json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(WithPrincipal(context.Background(), &Principal{Subject: user}))
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var response api.ApiResponse[json.RawMessage]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if status == 200 && rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return response.Data
	}
	pins := func(user, path, body string) []string {
		t.Helper()
		raw := request(user, "PUT", path, body, 200)
		var response api.AdminSkillPinResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if path == "/api/admin/skills/pin" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if len(fields) != 1 {
				t.Fatalf("pin response should only contain pinned: %s", raw)
			}
		}
		return response.Pinned
	}
	pins("alice", "/api/skills", `{"id":"center-extra","pinned":true}`)
	got := pins("alice", "/api/admin/skills/pin", `{"id":" OFFICE ","pinned":true}`)
	want := []string{"office", "center-extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pins=%v", got)
	}
	got = pins("alice", "/api/admin/skills/pin", `{"id":"center-extra","pinned":true}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repeat pin reordered: %v", got)
	}
	for _, user := range []string{"alice", "bob"} {
		var catalog api.AdminSkillsResponse
		if err := json.Unmarshal(request(user, "GET", "/api/admin/skills?userKey=alice", "", 200), &catalog); err != nil {
			t.Fatal(err)
		}
		if catalog.Skills == nil || catalog.Packages == nil || catalog.Pinned == nil {
			t.Fatal("arrays must not be null")
		}
		if user == "alice" && !reflect.DeepEqual(catalog.Pinned, want) {
			t.Fatal(catalog.Pinned)
		}
		if user == "bob" && len(catalog.Pinned) != 0 {
			t.Fatal("user preference leaked")
		}
		if len(catalog.Packages) != 1 || catalog.Packages[0].ID != "office" {
			t.Fatal(catalog.Packages)
		}
		member := findAdminSkillSummary(catalog.Skills, "office/mock-skill")
		if member == nil || member.PackageID != "office" {
			t.Fatal("missing package ownership")
		}
	}
	var usage api.AgentSkillsResponse
	_ = json.Unmarshal(request("alice", "GET", "/api/skills", "", 200), &usage)
	if !reflect.DeepEqual(usage.Pinned, want) {
		t.Fatalf("usage did not see admin pins: %v", usage.Pinned)
	}
	for _, body := range []string{`{}`, `{"id":"office"}`, `{"id":"office","pinned":null}`, `{"id":"../office","pinned":true}`} {
		request("alice", "PUT", "/api/admin/skills/pin", body, 400)
	}
	for _, id := range []string{"unknown", "private-only", "office/mock-skill"} {
		request("alice", "PUT", "/api/admin/skills/pin", `{"id":"`+id+`","pinned":true}`, 404)
	}
	pins("alice", "/api/admin/skills/pin", `{"id":"deleted/member","pinned":false}`)
	// Legacy package listing remains available to Desktop.
	legacy := getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", "/api/admin/skill-packages", nil)
	if len(legacy) != 1 {
		t.Fatal(legacy)
	}
	// A failed preference read fails the whole aggregate rather than returning a partial catalog.
	if err := os.WriteFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "order.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	request("alice", "GET", "/api/admin/skills", "", 500)
}

func TestAdminSkillCatalogEmptyPackageAndMissingMember(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f)
	result := getAPIData[api.AdminSkillsResponse](t, f.server, "GET", "/api/admin/skills", nil)
	if len(result.Packages) != 1 || len(result.Packages[0].Skills) != 0 {
		t.Fatal(result.Packages)
	}
	writeProjectionPackage(t, f, "mock-skill")
	if err := os.RemoveAll(filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "mock-skill")); err != nil {
		t.Fatal(err)
	}
	result = getAPIData[api.AdminSkillsResponse](t, f.server, "GET", "/api/admin/skills", nil)
	if result.Packages[0].Status != "incomplete" || !reflect.DeepEqual(result.Packages[0].MissingSkillIDs, []string{"office/mock-skill"}) {
		t.Fatal(result.Packages)
	}
}
