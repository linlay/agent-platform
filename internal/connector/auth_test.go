package connector

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthModeCanonicalAndLegacyManifests(t *testing.T) {
	for _, tc := range []struct {
		auth string
		want AuthMode
	}{
		{``, AuthDelegated}, {`,"auth_mode":null`, AuthDelegated},
		{`,"auth_mode":"none"`, AuthDelegated}, {`,"auth_mode":"cli"`, AuthDelegated},
		{`,"auth_mode":"oneid-token"`, AuthOneID}, {`,"auth_mode":"mcp"`, AuthMCP},
		{`,"auth_mode":"oauth","oauth":{"discovery":true}`, AuthMCP},
		{`,"auth_mode":"oauth","oauth":{"client_id":"demo"}`, AuthOAuth},
		{`,"auth_mode":"token","token_schema":{"fields":[{"key":"API_KEY","required":true}]}`, AuthToken},
	} {
		t.Run(tc.auth, func(t *testing.T) {
			data := []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp"` + tc.auth + `}`)
			if err := ValidateManifest("demo", data); err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err := DecodeJSON(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.normalizeAuth()
			if manifest.AuthMode != tc.want {
				t.Fatalf("mode=%q want %q", manifest.AuthMode, tc.want)
			}
			summary, err := json.Marshal(struct {
				Manifest
				Mounted bool `json:"mounted"`
			}{manifest, true})
			if err != nil || !strings.Contains(string(summary), `"mounted":true`) {
				t.Fatal("summary fields lost", err)
			}
			if tc.want == AuthDelegated && !strings.Contains(string(summary), `"auth_mode":null`) {
				t.Fatalf("not canonical: %s", summary)
			}
		})
	}
	for _, auth := range []string{`"null"`, `""`, `true`, `{}`, `"api-key"`} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":`+auth+`}`)); err == nil {
			t.Fatal("accepted invalid mode", auth)
		}
	}
}

func TestAuthFieldsCannotCrossModes(t *testing.T) {
	for _, fields := range []string{
		`"auth_mode":null,"oauth":{"discovery":true}`,
		`"auth_mode":"oneid-token","token_schema":{"fields":[{"key":"TOKEN"}]}`,
		`"auth_mode":"mcp","token_schema":{"fields":[{"key":"TOKEN"}]}`,
		`"auth_mode":"token","token_schema":null`,
		`"auth_mode":"token","token_schema":{"fields":[{"key":"TOKEN"},{"key":"TOKEN"}]}`,
	} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"mcp",`+fields+`}`)); err == nil {
			t.Fatal("accepted incompatible fields", fields)
		}
	}
}
