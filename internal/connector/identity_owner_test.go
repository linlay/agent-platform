package connector

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestDesktopIdentityOwnerComparison(t *testing.T) {
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://issuer.test","sub":"alice"}`)) + ".signature"
	h := sha256.Sum256([]byte(`["https://issuer.test","alice"]`))
	owner := "user:desktop-user:" + hex.EncodeToString(h[:])
	if !IdentityMatchesOwner(owner, token) {
		t.Fatal("same verified identity mismatched")
	}
	if IdentityMatchesOwner(owner, "opaque") || IdentityMatchesOwner(owner, "h."+base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://issuer.test","sub":"bob"}`))+".s") {
		t.Fatal("other account accepted")
	}
}
