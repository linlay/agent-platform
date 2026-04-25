package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRSAPrivateKeyPEMSupportsPKCS1(t *testing.T) {
	privateKey := mustGenerateRSAKey(t)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})

	parsed, err := parseRSAPrivateKeyPEM(pemData)
	if err != nil {
		t.Fatalf("parse pkcs1 private key: %v", err)
	}
	if parsed.D.Cmp(privateKey.D) != 0 {
		t.Fatal("parsed pkcs1 private key does not match original")
	}
}

func TestParseRSAPrivateKeyPEMSupportsPKCS8(t *testing.T) {
	privateKey := mustGenerateRSAKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal pkcs8 private key: %v", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	})

	parsed, err := parseRSAPrivateKeyPEM(pemData)
	if err != nil {
		t.Fatalf("parse pkcs8 private key: %v", err)
	}
	if parsed.D.Cmp(privateKey.D) != 0 {
		t.Fatal("parsed pkcs8 private key does not match original")
	}
}

func TestSignGatewayTokenSetsRequiredClaimsAndRS256Signature(t *testing.T) {
	privateKey := mustGenerateRSAKey(t)
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := signGatewayToken(privateKey, tokenClaims{
		Subject: "local",
		Issuer:  defaultGatewayTokenIssuer,
		Now:     now,
		TTL:     365 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("sign gateway token: %v", err)
	}

	header, payload, signature, signingInput := decodeJWT(t, token)
	if got := header["alg"]; got != "RS256" {
		t.Fatalf("header alg = %v, want RS256", got)
	}
	if got := payload["sub"]; got != "local" {
		t.Fatalf("payload sub = %v, want local", got)
	}
	if got := payload["iss"]; got != defaultGatewayTokenIssuer {
		t.Fatalf("payload iss = %v, want %s", got, defaultGatewayTokenIssuer)
	}
	exp, ok := payload["exp"].(float64)
	if !ok {
		t.Fatalf("payload exp has unexpected type %T", payload["exp"])
	}
	wantExp := now.Add(365 * 24 * time.Hour).Unix()
	if int64(exp) != wantExp {
		t.Fatalf("payload exp = %d, want %d", int64(exp), wantExp)
	}

	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, sum[:], signature); err != nil {
		t.Fatalf("verify signature: %v", err)
	}
}

func TestSignGatewayTokenRejectsInvalidInputs(t *testing.T) {
	privateKey := mustGenerateRSAKey(t)

	testCases := []struct {
		name   string
		key    *rsa.PrivateKey
		claims tokenClaims
	}{
		{
			name: "missing key",
			key:  nil,
			claims: tokenClaims{
				Subject: "local",
				Issuer:  defaultGatewayTokenIssuer,
				TTL:     time.Hour,
			},
		},
		{
			name: "missing subject",
			key:  privateKey,
			claims: tokenClaims{
				Issuer: defaultGatewayTokenIssuer,
				TTL:    time.Hour,
			},
		},
		{
			name: "missing issuer",
			key:  privateKey,
			claims: tokenClaims{
				Subject: "local",
				TTL:     time.Hour,
			},
		},
		{
			name: "non-positive ttl",
			key:  privateKey,
			claims: tokenClaims{
				Subject: "local",
				Issuer:  defaultGatewayTokenIssuer,
				TTL:     0,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := signGatewayToken(tc.key, tc.claims); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestParseRSAPrivateKeyPEMRejectsInvalidPEM(t *testing.T) {
	if _, err := parseRSAPrivateKeyPEM([]byte("not-a-pem")); err == nil {
		t.Fatal("expected invalid pem error")
	}
}

func TestLoadRSAPrivateKeyFileReadsFromDisk(t *testing.T) {
	privateKey := mustGenerateRSAKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-private-key.pem")
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	loaded, err := loadRSAPrivateKeyFile(path)
	if err != nil {
		t.Fatalf("load private key: %v", err)
	}
	if loaded.D.Cmp(privateKey.D) != 0 {
		t.Fatal("loaded private key does not match original")
	}
}

func TestLoadRSAPrivateKeyFileRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.pem")
	if err := os.WriteFile(path, []byte("bad data"), 0o600); err != nil {
		t.Fatalf("write invalid pem: %v", err)
	}

	if _, err := loadRSAPrivateKeyFile(path); err == nil {
		t.Fatal("expected load private key error")
	}
}

func decodeJWT(t *testing.T, token string) (map[string]any, map[string]any, []byte, string) {
	t.Helper()

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d, want 3", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	var header map[string]any
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return header, payload, signature, parts[0] + "." + parts[1]
}

func mustGenerateRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return key
}
