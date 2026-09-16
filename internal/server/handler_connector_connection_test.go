package server

import (
	"agent-platform/internal/connectorauth"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectorConnectionUsesPrincipalAndExplicitEnable(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	writeMCPConnectorForTest(t, fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir(), "demo")
	call := func(owner, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: owner}))
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := call("alice", "POST", "/api/connectors/connect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	read := func(owner string) connectorauth.Connection {
		r := call(owner, "GET", "/api/connectors/connection?id=demo&userKey=alice", "")
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct {
			Data connectorauth.Connection `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Data
	}
	a, b := read("alice"), read("bob")
	if !a.Bound || a.Enabled || b.Bound {
		t.Fatal(a, b)
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true,"userKey":"bob"}`); r.Code != 400 {
		t.Fatal("accepted forged owner", r.Code, r.Body.String())
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := call("alice", "POST", "/api/connectors/disconnect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if a := read("alice"); a.Bound || a.Enabled {
		t.Fatal(a)
	}
	if r := call("alice", "GET", "/api/connectors/connection", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"connections"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestConnectorOwnerRejectsDeviceIdentityWithoutVerifiedUser(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, subject := range []string{"app", "desktop-local:device", "desktop-user:short"} {
		ctx := WithPrincipal(context.Background(), &Principal{Subject: subject, Claims: map[string]any{"device_id": "device"}})
		if _, err := fixture.server.connectorOwnerUser(ctx); err == nil {
			t.Fatal("device acquired private connection owner", subject)
		}
	}
	subject := "desktop-user:" + strings.Repeat("a", 64)
	ctx := WithPrincipal(context.Background(), &Principal{Subject: subject, Claims: map[string]any{"device_id": "device"}})
	if owner, err := fixture.server.connectorOwnerUser(ctx); err != nil || owner != "user:"+subject {
		t.Fatal(owner, err)
	}
}
