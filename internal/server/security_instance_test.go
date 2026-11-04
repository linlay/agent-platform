package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestJWTVerifierInstanceBinding(t *testing.T) {
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, t.TempDir())
	future := time.Now().Add(time.Hour).Unix()
	base := config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicKeyPath}
	bound := base
	bound.Audience = "instance-a"
	bound.AllowedSubjects = []string{"owner"}
	bound.RequireExpiry = true

	cases := []struct {
		name    string
		cfg     config.AuthConfig
		payload map[string]any
		wantErr string
	}{
		{"unbound instance keeps accepting tokens without aud or exp", base, map[string]any{"sub": "anyone"}, ""},
		{"owner with list audience", bound, map[string]any{"sub": "owner", "aud": []string{"other", "instance-a"}, "exp": future}, ""},
		{"owner with string audience", bound, map[string]any{"sub": "owner", "aud": "instance-a", "exp": future}, ""},
		{"another user", bound, map[string]any{"sub": "intruder", "aud": "instance-a", "exp": future}, "jwt subject not allowed"},
		{"missing subject", bound, map[string]any{"aud": "instance-a", "exp": future}, "jwt subject not allowed"},
		{"token for another instance", bound, map[string]any{"sub": "owner", "aud": []string{"instance-b"}, "exp": future}, "jwt audience mismatch"},
		{"missing audience", bound, map[string]any{"sub": "owner", "exp": future}, "jwt audience mismatch"},
		{"missing expiry", bound, map[string]any{"sub": "owner", "aud": "instance-a"}, "jwt expiry required"},
		{"zero expiry", bound, map[string]any{"sub": "owner", "aud": "instance-a", "exp": 0}, "jwt expiry required"},
		{"expired", bound, map[string]any{"sub": "owner", "aud": "instance-a", "exp": time.Now().Add(-time.Minute).Unix()}, "jwt expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJWTVerifier(tc.cfg).Verify(mustSignRS256JWT(t, privateKey, tc.payload))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Verify error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRequestBaseURLPrefersConfiguredPublicOrigin(t *testing.T) {
	req := httptest.NewRequest("POST", "http://internal:8080/api/query", nil)
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	req.Header.Set("X-Forwarded-Proto", "https")

	fixed := &Server{deps: Dependencies{Config: config.Config{Server: config.ServerConfig{PublicBaseURL: "https://linlay.ap.example.com"}}}}
	if got := fixed.requestBaseURL(req); got != "https://linlay.ap.example.com" {
		t.Fatalf("configured origin = %q", got)
	}
	derived := &Server{}
	if got := derived.requestBaseURL(req); got != "https://attacker.example" {
		t.Fatalf("header-derived origin = %q", got)
	}
}
