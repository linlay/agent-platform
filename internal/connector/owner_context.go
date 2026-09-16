package connector

import "context"

type ownerContextKey struct{}

func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerContextKey{}, owner)
}
func OwnerFromContext(ctx context.Context) string {
	owner, _ := ctx.Value(ownerContextKey{}).(string)
	return owner
}
