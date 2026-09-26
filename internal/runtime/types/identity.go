package types

import (
	"context"

	"agent-platform/internal/contracts"
)

type identityKey struct{}

func WithIdentity(ctx context.Context, identity *contracts.AuthIdentity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}
func IdentityFromContext(ctx context.Context) *contracts.AuthIdentity {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(identityKey{}).(*contracts.AuthIdentity)
	return v
}

type sourceKey struct{}

func WithChatSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}
func ChatSourceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(sourceKey{}).(string)
	return s
}
