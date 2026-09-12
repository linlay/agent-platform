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
