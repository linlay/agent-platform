package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueryHTTPRejectsUnknownFieldsBeforeRunAdmission(t *testing.T) {
	fixture := newTestFixture(t)
	for _, value := range []string{"null", "[]", `"option"`} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/query?detail=full", strings.NewReader(`{"message":"hello","unexpectedOption":`+value+`}`))
		fixture.server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "unknown field") {
			t.Fatalf("value=%s status=%d body=%s", value, recorder.Code, recorder.Body.String())
		}
	}
}
