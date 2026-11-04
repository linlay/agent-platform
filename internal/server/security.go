package server

import "agent-platform/internal/authn"
import (
	"agent-platform/internal/config"
	runtimetypes "agent-platform/internal/runtime/types"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type principalContextKey struct{}
type ResourceTicketService struct{ cfg config.ResourceTicketConfig }

func NewResourceTicketService(cfg config.ResourceTicketConfig) *ResourceTicketService {
	return &ResourceTicketService{cfg: cfg}
}

func WithPrincipal(ctx context.Context, principal *authn.Principal) context.Context {
	return context.WithValue(runtimetypes.WithIdentity(ctx, buildAuthIdentity(principal)), principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) *authn.Principal {
	value, _ := ctx.Value(principalContextKey{}).(*authn.Principal)
	return value
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
	if !s.cfg.Enabled() {
		return "", fmt.Errorf("resource ticket disabled")
	}
	header, payload, signature, signingInput, err := authn.ParseJWT(token)
	if err != nil {
		return "", err
	}
	if authn.StringClaim(header, "alg") != "HS256" {
		return "", fmt.Errorf("invalid resource ticket alg")
	}
	expected := computeHMACSHA256(signingInput, s.cfg.Secret)
	if !hmac.Equal(signature, expected) {
		return "", fmt.Errorf("resource ticket invalid")
	}
	if expiration := authn.NumericDate(payload["e"]); expiration > 0 && time.Now().Unix() >= expiration {
		return "", fmt.Errorf("resource ticket expired")
	}
	chatID := authn.StringClaim(payload, "c")
	if chatID == "" {
		return "", fmt.Errorf("resource ticket invalid")
	}
	return chatID, nil
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

func maxInt64(value int64, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func (s *ResourceTicketService) Enabled() bool { return s != nil && s.cfg.Enabled() }
