package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func TestConnectorProtocolErrorsPreserveFailureCategory(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{connector.ErrInvalidPackage, http.StatusBadRequest, "invalid_package"},
		{connectorauth.ErrOAuthClientSetupRequired, http.StatusConflict, "connector_setup_required"},
		{connectorauth.ErrOAuthAuthorizationRequired, http.StatusUnauthorized, "connector_auth_required"},
		{connectorauth.ErrOAuthRefreshFailed, http.StatusServiceUnavailable, "connector_execution_failed"},
		{connectorauth.ErrOAuthCredentialUnavailable, http.StatusServiceUnavailable, "connector_execution_failed"},
	} {
		rec := httptest.NewRecorder()
		fixture.server.writeConnectorError(rec, tc.err)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("category %s: %d %s", tc.code, rec.Code, rec.Body.String())
		}
	}
}
