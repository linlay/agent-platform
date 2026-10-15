package connector

import "testing"

func TestAuthorizationBrowserManifest(t *testing.T) {
	for _, tc := range []struct {
		field string
		valid bool
		want  string
	}{
		{``, true, "system"}, {`,"authBrowser":"system"`, true, "system"}, {`,"authBrowser":"embedded"`, true, "embedded"},
		{`,"authBrowser":"auto"`, false, ""}, {`,"authBrowser":true`, false, ""},
	} {
		data := []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"self"` + tc.field + `}`)
		err := ValidateManifest("demo", data)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.field, err)
		}
		if tc.valid {
			var m Manifest
			if err := DecodeJSON(data, &m); err != nil {
				t.Fatal(err)
			}
			if m.AuthorizationBrowser() != tc.want {
				t.Fatal(m.AuthorizationBrowser())
			}
		}
	}
}

func TestAuthorizationBrowserComesFromManifestOnly(t *testing.T) {
	for _, tc := range []struct{ manifest, want string }{{"", "system"}, {"system", "system"}, {"embedded", "embedded"}} {
		pkg := Package{Manifest: Manifest{AuthMode: AuthDelegated, AuthBrowser: tc.manifest}, CLI: map[string]any{"auth": map[string]any{}}}
		if got := pkg.AuthorizationBrowser(); got != tc.want {
			t.Fatalf("authBrowser %q: got %s, want %s", tc.manifest, got, tc.want)
		}
	}
}
