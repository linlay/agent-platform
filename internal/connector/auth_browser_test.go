package connector

import "testing"

func TestAuthorizationBrowserManifest(t *testing.T) {
	for _, tc := range []struct {
		field string
		valid bool
		want  string
	}{
		{``, true, "system"}, {`,"auth_browser":"system"`, true, "system"}, {`,"auth_browser":"embedded"`, true, "embedded"},
		{`,"auth_browser":"auto"`, false, ""}, {`,"auth_browser":true`, false, ""},
	} {
		data := []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":null` + tc.field + `}`)
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

func TestAuthorizationBrowserPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		legacy   any
		want     string
	}{
		{"default", "", nil, "system"},
		{"legacy embedded", "", true, "embedded"},
		{"legacy system", "", false, "system"},
		{"legacy string ignored", "", "true", "system"},
		{"explicit system wins", "system", true, "system"},
		{"explicit embedded wins", "embedded", false, "embedded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := Package{Manifest: Manifest{AuthBrowser: tc.manifest}, CLI: map[string]any{"authQrModal": tc.legacy}}
			if got := pkg.AuthorizationBrowser(); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
