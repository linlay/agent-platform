package server

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/config"
)

type principalContextKey struct{}

type Principal struct {
	Subject string
	Claims  map[string]any
}

type JWTVerifier struct {
	cfg    config.AuthConfig
	client *http.Client

	mu             sync.Mutex
	cachedAt       time.Time
	cachedKeys     map[string]*rsa.PublicKey
	cachedLocal    *rsa.PublicKey
	cachedLocalErr error
}

type ResourceTicketService struct {
	cfg config.ChatImageTokenConfig
}

func NewJWTVerifier(cfg config.AuthConfig) *JWTVerifier {
	return &JWTVerifier{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func NewResourceTicketService(cfg config.ChatImageTokenConfig) *ResourceTicketService {
	return &ResourceTicketService{cfg: cfg}
}

func WithPrincipal(ctx context.Context, principal *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) *Principal {
	value, _ := ctx.Value(principalContextKey{}).(*Principal)
	return value
}

func (v *JWTVerifier) Verify(token string) (*Principal, error) {
	header, payload, signature, signingInput, err := parseJWT(token)
	if err != nil {
		return nil, err
	}
	alg := stringClaim(header, "alg")
	if alg != "RS256" {
		return nil, fmt.Errorf("unsupported jwt alg: %s", alg)
	}

	key, err := v.resolveKey(stringClaim(header, "kid"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature); err != nil {
		return nil, fmt.Errorf("jwt signature verification failed")
	}

	if iss := strings.TrimSpace(v.cfg.Issuer); iss != "" && stringClaim(payload, "iss") != iss {
		return nil, fmt.Errorf("jwt issuer mismatch")
	}
	if exp, ok := payload["exp"]; ok {
		if expiration := numericDate(exp); expiration > 0 && time.Now().Unix() >= expiration {
			return nil, fmt.Errorf("jwt expired")
		}
	}

	subject := stringClaim(payload, "sub")
	if subject == "" {
		subject = stringClaim(payload, "uid")
	}
	if subject == "" {
		subject = stringClaim(payload, "userId")
	}
	return &Principal{
		Subject: subject,
		Claims:  payload,
	}, nil
}

func (v *JWTVerifier) resolveKey(kid string) (*rsa.PublicKey, error) {
	if strings.TrimSpace(v.cfg.LocalPublicKeyFile) != "" {
		return v.localKey()
	}
	if strings.TrimSpace(v.cfg.JWKSURI) == "" {
		return nil, fmt.Errorf("jwt verification not configured")
	}
	return v.jwksKey(kid)
}

func (v *JWTVerifier) localKey() (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cachedLocal != nil || v.cachedLocalErr != nil {
		return v.cachedLocal, v.cachedLocalErr
	}
	data, err := os.ReadFile(v.cfg.LocalPublicKeyFile)
	if err != nil {
		v.cachedLocalErr = err
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		v.cachedLocalErr = fmt.Errorf("invalid public key pem")
		return nil, v.cachedLocalErr
	}
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			v.cachedLocalErr = fmt.Errorf("public key is not RSA")
			return nil, v.cachedLocalErr
		}
		v.cachedLocal = rsaKey
		return rsaKey, nil
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		rsaKey, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			v.cachedLocalErr = fmt.Errorf("certificate public key is not RSA")
			return nil, v.cachedLocalErr
		}
		v.cachedLocal = rsaKey
		return rsaKey, nil
	}
	v.cachedLocalErr = fmt.Errorf("unsupported public key format")
	return nil, v.cachedLocalErr
}

func (v *JWTVerifier) jwksKey(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.cachedKeys) == 0 || v.jwksExpiredLocked() {
		if err := v.refreshJWKSLocked(); err != nil {
			return nil, err
		}
	}
	if kid != "" {
		if key, ok := v.cachedKeys[kid]; ok {
			return key, nil
		}
	}
	for _, key := range v.cachedKeys {
		return key, nil
	}
	return nil, fmt.Errorf("no jwks rsa key available")
}

func (v *JWTVerifier) jwksExpiredLocked() bool {
	cacheSeconds := v.cfg.JWKSCacheSeconds
	if cacheSeconds <= 0 {
		cacheSeconds = 300
	}
	return time.Since(v.cachedAt) > time.Duration(cacheSeconds)*time.Second
}

func (v *JWTVerifier) refreshJWKSLocked() error {
	resp, err := v.client.Get(v.cfg.JWKSURI)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jwks fetch failed: %d", resp.StatusCode)
	}
	var payload struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, item := range payload.Keys {
		if item.Kty != "RSA" {
			continue
		}
		key, err := jwksRSAPublicKey(item.N, item.E)
		if err != nil {
			continue
		}
		keys[item.Kid] = key
	}
	v.cachedKeys = keys
	v.cachedAt = time.Now()
	return nil
}

func (s *ResourceTicketService) Issue(uid string, chatID string) string {
	if strings.TrimSpace(uid) == "" || strings.TrimSpace(chatID) == "" || strings.TrimSpace(s.cfg.Secret) == "" {
		return ""
	}
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	payload := map[string]any{
		"u": uid,
		"c": chatID,
		"e": time.Now().Add(time.Duration(maxInt64(s.cfg.TTLSeconds, 60)) * time.Second).Unix(),
	}
	token, err := signHS256JWT(header, payload, s.cfg.Secret)
	if err != nil {
		return ""
	}
	return token
}

func (s *ResourceTicketService) Verify(token string) (string, error) {
	header, payload, signature, signingInput, err := parseJWT(token)
	if err != nil {
		return "", err
	}
	if stringClaim(header, "alg") != "HS256" {
		return "", fmt.Errorf("invalid resource ticket alg")
	}
	expected := computeHMACSHA256(signingInput, s.cfg.Secret)
	if !hmac.Equal(signature, expected) {
		return "", fmt.Errorf("resource ticket invalid")
	}
	if expiration := numericDate(payload["e"]); expiration > 0 && time.Now().Unix() >= expiration {
		return "", fmt.Errorf("resource ticket expired")
	}
	chatID := stringClaim(payload, "c")
	if chatID == "" {
		return "", fmt.Errorf("resource ticket invalid")
	}
	return chatID, nil
}

func parseJWT(token string) (map[string]any, map[string]any, []byte, string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, nil, nil, "", fmt.Errorf("invalid jwt format")
	}
	headerData, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, "", err
	}
	payloadData, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, nil, nil, "", err
	}
	var header map[string]any
	if err := json.Unmarshal(headerData, &header); err != nil {
		return nil, nil, nil, "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadData, &payload); err != nil {
		return nil, nil, nil, "", err
	}
	return header, payload, signature, parts[0] + "." + parts[1], nil
}

func jwksRSAPublicKey(modulus string, exponent string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	if e == 0 {
		return nil, errors.New("invalid jwks exponent")
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: e,
	}, nil
}

func signHS256JWT(header map[string]any, payload map[string]any, secret string) (string, error) {
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	headerPart := base64.RawURLEncoding.EncodeToString(headerBytes)
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := headerPart + "." + payloadPart
	signature := base64.RawURLEncoding.EncodeToString(computeHMACSHA256(signingInput, secret))
	return signingInput + "." + signature, nil
}

func computeHMACSHA256(signingInput string, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

func stringClaim(claims map[string]any, key string) string {
	switch value := claims[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return fmt.Sprintf("%.0f", value)
	case int64:
		return fmt.Sprintf("%d", value)
	case int:
		return fmt.Sprintf("%d", value)
	default:
		return ""
	}
}

func numericDate(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		var out int64
		_, _ = fmt.Sscanf(strings.TrimSpace(v), "%d", &out)
		return out
	default:
		return 0
	}
}

func maxInt64(value int64, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}
