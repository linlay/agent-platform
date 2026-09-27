package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/connectorauth"
)

func TestConnectorConfigurationIsSharedAcrossPrincipals(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	writeMCPConnectorForTest(t, f.server.deps.Config.Paths.EffectiveConnectorsCenterDir(), "demo")
	call := func(owner, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: owner}))
		r := httptest.NewRecorder()
		f.server.ServeHTTP(r, req)
		return r
	}
	read := func(owner string) connectorauth.Connection {
		r := call(owner, "GET", "/api/connectors/connection?id=demo", "")
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct {
			Data connectorauth.Connection `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.Body.String(), `"enabled"`) || strings.Contains(r.Body.String(), `"bound"`) {
			t.Fatal("legacy switch in response", r.Body.String())
		}
		return v.Data
	}
	if read("alice").Configured {
		t.Fatal("missing configuration defaults ready")
	}
	if r := call("alice", "POST", "/api/connectors/connect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if s := read("bob"); !s.Configured || s.Readiness != "ready" {
		t.Fatal(s)
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 405 {
		t.Fatal("enable API retained", r.Code)
	}
	if r := call("alice", "POST", "/api/connectors/check?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := call("alice", "POST", "/api/connectors/disconnect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if read("bob").Configured {
		t.Fatal("disconnect retained configuration")
	}
	for _, path := range []string{"/api/connectors/connect?id=demo", "/api/connectors/disconnect?id=demo", "/api/connectors/check?id=demo"} {
		if r := call("alice", "GET", path, ""); r.Code != 405 {
			t.Fatal(r.Code)
		}
	}
	if r := call("alice", "GET", "/api/connectors/connection", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"connections"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestNoAuthConnectionHTTP(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	root := f.server.deps.Config.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	path := filepath.Join(root, "demo", "connector.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["auth_mode"] = "no_auth"
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: "owner"}))
		response := httptest.NewRecorder()
		f.server.ServeHTTP(response, req)
		return response
	}
	response := call("GET", "/api/connectors/connection?id=demo")
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var envelope struct{ Data connectorauth.Connection }
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	c := envelope.Data
	if c.ConfigurationRequired || c.Capabilities.CanConnect || c.Capabilities.CanDisconnect || c.Capabilities.CanCheck || c.Readiness != "no_auth" || c.Authentication.Status != "no_auth" {
		t.Fatalf("%#v", c)
	}
	for _, action := range []string{"connect", "disconnect", "check"} {
		response = call("POST", "/api/connectors/"+action+"?id=demo")
		if response.Code != 409 || !strings.Contains(response.Body.String(), "connector_auth_not_required") {
			t.Fatal(action, response.Code, response.Body.String())
		}
	}
}
