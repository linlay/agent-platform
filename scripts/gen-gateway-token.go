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
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultGatewayTokenIssuer = "agent-platform"

func main() {
	var (
		keyPath = flag.String("key", "", "path to RSA private key PEM file")
		subject = flag.String("sub", "", "JWT subject; must match gateway userId")
		issuer  = flag.String("iss", defaultGatewayTokenIssuer, "JWT issuer")
		ttl     = flag.Duration("ttl", 365*24*time.Hour, "token lifetime")
	)
	flag.Parse()

	if strings.TrimSpace(*keyPath) == "" {
		exitWithError("missing required -key")
	}
	if strings.TrimSpace(*subject) == "" {
		exitWithError("missing required -sub")
	}
	if *ttl <= 0 {
		exitWithError("-ttl must be greater than 0")
	}

	privateKey, err := loadRSAPrivateKeyFile(*keyPath)
	if err != nil {
		exitWithError(err.Error())
	}
	token, err := signGatewayToken(privateKey, tokenClaims{
		Subject: strings.TrimSpace(*subject),
		Issuer:  strings.TrimSpace(*issuer),
		Now:     time.Now(),
		TTL:     *ttl,
	})
	if err != nil {
		exitWithError(err.Error())
	}

	fmt.Println(token)
}

type tokenClaims struct {
	Subject string
	Issuer  string
	Now     time.Time
	TTL     time.Duration
}

func signGatewayToken(privateKey *rsa.PrivateKey, claims tokenClaims) (string, error) {
	if privateKey == nil {
		return "", fmt.Errorf("private key is required")
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return "", fmt.Errorf("subject is required")
	}
	if strings.TrimSpace(claims.Issuer) == "" {
		return "", fmt.Errorf("issuer is required")
	}
	if claims.TTL <= 0 {
		return "", fmt.Errorf("ttl must be greater than 0")
	}
	if claims.Now.IsZero() {
		claims.Now = time.Now()
	}

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(map[string]any{
		"sub": claims.Subject,
		"iss": claims.Issuer,
		"exp": claims.Now.Add(claims.TTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func loadRSAPrivateKeyFile(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	key, err := parseRSAPrivateKeyPEM(data)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}
	return key, nil
}

func parseRSAPrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	rest := data
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		switch block.Type {
		case "RSA PRIVATE KEY":
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err == nil {
				return key, nil
			}
		case "PRIVATE KEY":
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err == nil {
				rsaKey, ok := key.(*rsa.PrivateKey)
				if !ok {
					return nil, fmt.Errorf("private key is not RSA")
				}
				return rsaKey, nil
			}
		}
	}
	return nil, fmt.Errorf("unsupported private key format")
}

func exitWithError(message string) {
	_, _ = fmt.Fprintf(os.Stderr, "gen-gateway-token: %s\n", message)
	os.Exit(1)
}
