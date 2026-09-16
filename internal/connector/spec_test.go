package connector

import "testing"

func TestSpecManifestRequiresExplicitCurrentAuthentication(t *testing.T) {
	for _, raw := range []string{`{"auth_mode":null}`, `{"auth_mode":"token"}`, `{"auth_mode":"oneid-token"}`} {
		if err := ValidateSpecManifest([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `{"auth_mode":"none"}`, `{"auth_mode":"null"}`, `{"auth_mode":null,"auth_mode":null}`} {
		if err := ValidateSpecManifest([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestExternalCLISpecRejectsLegacyAndReservedEnvironment(t *testing.T) {
	oscmd := map[string]any{"darwin": "demo auth", "linux": "demo auth", "win32": "demo auth"}
	for _, cli := range []map[string]any{{"platform": map[string]any{}}, {"authQrModal": true}, {"auth": oscmd}, {"init": map[string]any{"windows": "npm install"}}, {"staticEnv": map[string]any{"HOME": "/private"}}, {"env": map[string]any{"BAD": "${TOKEN}"}}} {
		if err := ValidateExternalCLI(Package{Manifest: Manifest{Type: "cli"}, CLI: cli}); err == nil {
			t.Fatalf("accepted %#v", cli)
		}
	}
	if err := ValidateExternalCLI(Package{Manifest: Manifest{Type: "cli"}, CLI: map[string]any{"auth": oscmd, "status": oscmd, "unAuth": oscmd}}); err != nil {
		t.Fatal(err)
	}
}
