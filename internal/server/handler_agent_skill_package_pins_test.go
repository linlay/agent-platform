package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalogorder"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestAgentSkillPackagePinsHTTPPreserveSharedOrderAndExecutionBoundary(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "center-extra")
	orderPath := filepath.Join(f.cfg.Paths.SkillsCenterDir, "order.json")
	// Existing standalone/member preferences retain the version-1 string-array format.
	legacy := `{"version":1,"users":{"local":{"order":["office/center-extra","center-extra"],"updatedAt":123},"user:other":{"order":["mock-skill"],"updatedAt":456}}}`
	if err := os.WriteFile(orderPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	before := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil)
	want := []string{"office", "office/center-extra", "center-extra"}
	pinned := getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(`{"id":" OFFICE ","pinned":true}`))
	if !reflect.DeepEqual(pinned.Pinned, want) || pinned.Skills == nil || len(pinned.Skills) != 0 || !reflect.DeepEqual(pinned.Packages, before.Packages) {
		t.Fatalf("package pin changed members or response contract: %+v", pinned)
	}
	for _, path := range []string{"/api/skills", "/api/skills?agentKey=mock-agent"} {
		response := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", path, nil)
		if !reflect.DeepEqual(response.Pinned, want) {
			t.Fatalf("shared pins at %s: %+v", path, response.Pinned)
		}
		for _, skill := range response.Skills {
			if skill.ID == "office" {
				t.Fatal("package pin became an executable catalog entry")
			}
		}
	}
	if _, found := f.server.deps.Registry.SkillDefinition("office"); found {
		t.Fatal("package ID resolved as an executable skill")
	}
	definition, _ := f.server.deps.Registry.AgentDefinition("mock-agent")
	if _, err := resolveMustUseSkills(definition, f.cfg.Paths.SkillsCenterDir, f.server.deps.Registry, []string{"office"}); err == nil {
		t.Fatal("pinned package ID accepted by mustUseSkills")
	}
	if resolved, err := resolveMustUseSkills(definition, f.cfg.Paths.SkillsCenterDir, f.server.deps.Registry, []string{"office/center-extra", "center-extra"}); err != nil || !reflect.DeepEqual(resolved.IDs, []string{"office/center-extra", "center-extra"}) {
		t.Fatalf("concrete skill identities changed: %+v %v", resolved, err)
	}
	// A fresh store reads the same mixed order without migration or changing other users.
	store := catalogorder.NewFileOrderStore(f.cfg.Paths.SkillsCenterDir)
	if state, err := store.Read("local"); err != nil || !reflect.DeepEqual(state.Order, want) {
		t.Fatalf("stored order: %+v %v", state, err)
	}
	if other, err := store.Read("user:other"); err != nil || !reflect.DeepEqual(other.Order, []string{"mock-skill"}) || other.UpdatedAt != 456 {
		t.Fatalf("other user's pins changed: %+v %v", other, err)
	}
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`{"id":"office/center-extra","pinned":true}`, want},
		{`{"id":"office","pinned":true}`, want},
		{`{"id":"office/center-extra","pinned":false}`, []string{"office", "center-extra"}},
		{`{"id":"office/center-extra","pinned":true}`, []string{"office/center-extra", "office", "center-extra"}},
		{`{"id":"office","pinned":false}`, []string{"office/center-extra", "center-extra"}},
	} {
		response := getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(tc.body))
		if !reflect.DeepEqual(response.Pinned, tc.want) {
			t.Fatalf("%s: pins=%v want=%v", tc.body, response.Pinned, tc.want)
		}
	}
	if after := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil); !reflect.DeepEqual(after.Skills, before.Skills) {
		t.Fatalf("pin mutation changed the skill catalog: before=%+v after=%+v", before.Skills, after.Skills)
	}
}

func TestAgentSkillPackagePinsRejectMissingOrInvalidPackagesAndAllowCleanup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		remove   bool
	}{
		{name: "deleted directory", remove: true},
		{name: "invalid JSON", manifest: `{`},
		{name: "missing name", manifest: `{}`},
		{name: "mismatched name", manifest: `{"name":"another-package"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentSkillsTestFixture(t, false)
			writeProjectionPackage(t, f, "center-extra")
			getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(`{"id":"office","pinned":true}`))
			root := filepath.Join(f.cfg.Paths.SkillsCenterDir, "office")
			var err error
			if tc.remove {
				err = os.RemoveAll(root)
			} else {
				err = os.WriteFile(filepath.Join(root, "package.json"), []byte(tc.manifest), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Validate current disk metadata, even without a catalog reload.
			recorder := httptest.NewRecorder()
			f.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/skills", strings.NewReader(`{"id":"office","pinned":true}`)))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("invalid package pin: %d %s", recorder.Code, recorder.Body.String())
			}
			response := getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(`{"id":"office","pinned":false}`))
			if response.Pinned == nil || len(response.Pinned) != 0 {
				t.Fatalf("stale package pin was not removed: %+v", response.Pinned)
			}
		})
	}
}

func TestAgentSkillPackagePinsWebSocketShareHTTPOrderAndValidatePackages(t *testing.T) {
	f := newAgentSkillsTestFixture(t, true)
	writeProjectionPackage(t, f, "center-extra")
	getAPIData[api.AgentSkillsResponse](t, f.server, "PUT", "/api/skills", []byte(`{"id":"center-extra","pinned":true}`))
	server := httptest.NewServer(f.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)
	for _, tc := range []struct {
		id      string
		payload map[string]any
		want    []string
	}{
		{"pin-package", map[string]any{"id": "office", "pinned": true}, []string{"office", "center-extra"}},
		{"pin-member", map[string]any{"id": "office/center-extra", "pinned": true}, []string{"office/center-extra", "office", "center-extra"}},
		{"unpin-package", map[string]any{"id": "office", "pinned": false}, []string{"office/center-extra", "center-extra"}},
		{"read", map[string]any{}, []string{"office/center-extra", "center-extra"}},
	} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: tc.id, Payload: marshalPayload(tc.payload)}); err != nil {
			t.Fatal(err)
		}
		response := waitForWebSocketResponseData[api.AgentSkillsResponse](t, conn, tc.id)
		httpResponse := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", "/api/skills", nil)
		if !reflect.DeepEqual(response.Pinned, tc.want) || !reflect.DeepEqual(response.Pinned, httpResponse.Pinned) || !reflect.DeepEqual(response.Packages, httpResponse.Packages) {
			t.Fatalf("%s: WS=%+v HTTP=%+v", tc.id, response, httpResponse)
		}
	}
	if err := os.WriteFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "package.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"office", "missing-package"} {
		if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/skills", ID: key, Payload: marshalPayload(map[string]any{"id": key, "pinned": true})}); err != nil {
			t.Fatal(err)
		}
		var frame ws.ErrorFrame
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Frame != ws.FrameError || frame.Code != http.StatusNotFound {
			encoded, _ := json.Marshal(frame)
			t.Fatalf("%s: %s", key, encoded)
		}
	}
}
