package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// IdentityMatchesOwner prevents a stale Desktop Platform JWT from receiving
// another signed-in user's current OneID token. Parsing is not signature verification:
// the identity file is provided by Desktop after its existing SSO verification.
func IdentityMatchesOwner(owner, token string) bool {
	owner = strings.TrimPrefix(owner, "user:")
	if !strings.HasPrefix(owner, "desktop-user:") {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Issuer  string `json:"iss"`
		Subject string `json:"sub"`
	}
	if json.Unmarshal(data, &claims) != nil || claims.Issuer == "" || claims.Subject == "" {
		return false
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode([]string{claims.Issuer, claims.Subject}) != nil {
		return false
	}
	digest := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return owner == "desktop-user:"+hex.EncodeToString(digest[:])
}
