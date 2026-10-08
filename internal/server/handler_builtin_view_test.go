package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/view"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestUnifiedViewHTTPBuiltinAndRemovedRoute(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/view?source=builtin&key=platform_control_review", 200},
		{"/api/view?source=builtin&key=question", 404},
		{"/api/view?source=builtin&key=missing", 404},
		{"/api/view?key=platform_control_review", 400},
		{"/api/view?source=builtin&key=question&connectorId=other", 400},
		{"/api/viewport?viewportKey=platform_control_review", 404},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
		if rec.Code == 200 {
			var result api.ApiResponse[view.Document]
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.View.Source != "builtin" || result.Data.HTML == "" {
				t.Fatalf("%#v", result)
			}
		}
	}
}
