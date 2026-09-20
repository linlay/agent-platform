package server

import "context"

func withCatalogTransaction[T any](ctx context.Context, s *Server, mutate func(context.Context) (T, error)) (result T, err error) {
	if coordinator, ok := s.deps.CatalogReloader.(interface {
		WithCatalogMutation(context.Context, func(context.Context) error) error
	}); ok {
		err = coordinator.WithCatalogMutation(ctx, func(ctx context.Context) error {
			var mutationErr error
			result, mutationErr = mutate(ctx)
			return mutationErr
		})
		return result, err
	}
	return mutate(ctx)
}
