package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestDesktopMigrationRequiresSignedCurrentCanonicalIdentity(t *testing.T) {
	root := t.TempDir()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: mustMigrationPublicKey(t, &private.PublicKey)})
	publicPath := filepath.Join(root, "public.pem")
	os.WriteFile(publicPath, public, 0600)
	canonicalClaims := map[string]any{"iss": "https://canonical.example", "sub": "alice"}
	encoded, _ := json.Marshal(canonicalClaims)
	canonical := "header." + base64.RawURLEncoding.EncodeToString(encoded) + ".signature"
	identityPath := filepath.Join(root, "identity")
	os.WriteFile(identityPath, []byte(canonical), 0600)
	identityJSON, _ := json.Marshal([]string{"https://canonical.example", "alice"})
	sum := sha256.Sum256(identityJSON)
	subject := "desktop-user:" + hex.EncodeToString(sum[:])
	cfg := config.Config{RuntimeMode: config.RuntimeModeDesktop, IdentityFile: identityPath, Auth: config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicPath, Issuer: "local-platform"}}
	claims := map[string]any{"iss": "local-platform", "sub": subject, "device_id": "current-device", "exp": time.Now().Add(time.Hour).Unix()}
	token := migrationSignedJWT(t, private, claims)
	got, device, err := verifyDesktopMigrationIdentity(cfg, token)
	if err != nil || got != subject || device != "current-device" {
		t.Fatal(got, device, err)
	}
	for _, change := range []map[string]any{{"sub": "app"}, {"sub": "desktop-user:" + strings.Repeat("b", 64)}, {"device_id": ""}, {"exp": time.Now().Add(-time.Hour).Unix()}, {"iss": "other-issuer"}} {
		bad := map[string]any{}
		for k, v := range claims {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		if _, _, err := verifyDesktopMigrationIdentity(cfg, migrationSignedJWT(t, private, bad)); err == nil {
			t.Fatalf("accepted wrong identity %+v", change)
		}
	}
	if _, _, err := verifyDesktopMigrationIdentity(cfg, token+"tampered"); err == nil {
		t.Fatal("accepted tampered signature")
	}
	os.WriteFile(identityPath, []byte("header."+base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://canonical.example","sub":"bob"}`))+".signature"), 0600)
	if _, _, err := verifyDesktopMigrationIdentity(cfg, token); err == nil {
		t.Fatal("accepted stale Desktop user")
	}
}
func mustMigrationPublicKey(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	value, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func migrationSignedJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	raw, _ := json.Marshal(claims)
	input := header + "." + base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestDesktopMigrationReceiptCannotBeClaimedByAnotherUser(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{StateDir: filepath.Join(root, "state"), ChatsDir: filepath.Join(root, "chats"), SkillsCenterDir: filepath.Join(root, "skills"), ConnectorsCenterDir: filepath.Join(root, "connectors")}}
	subject := "desktop-user:" + strings.Repeat("a", 64)
	receipt, err := migrateDesktopOwner(t.Context(), cfg, subject, "device")
	if err != nil || receipt.CompletedAt == "" {
		t.Fatal(receipt, err)
	}
	again, err := migrateDesktopOwner(t.Context(), cfg, subject, "device")
	if err != nil || again.CompletedAt != receipt.CompletedAt {
		t.Fatal("same user was not idempotent", again, err)
	}
	if _, err := migrateDesktopOwner(t.Context(), cfg, "desktop-user:"+strings.Repeat("b", 64), "device"); err == nil {
		t.Fatal("another account claimed legacy data")
	}
	if _, err := migrateDesktopOwner(t.Context(), cfg, subject, "other-device"); err == nil {
		t.Fatal("another device claimed legacy data")
	}
}
