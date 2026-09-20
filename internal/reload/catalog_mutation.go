package reload

import (
	"context"
	"errors"
	"fmt"
)

type mutationContextKey struct{}

// WithCatalogMutation serializes managed writes and snapshots with catalog
// publication. It does not stop directory monitoring. Callbacks must not retain
// ctx or use its reentrant publication privilege from another goroutine.
func (r *RuntimeCatalogReloader) WithCatalogMutation(ctx context.Context, mutate func(context.Context) error) error {
	if ctx.Value(mutationContextKey{}) == r {
		return mutate(ctx)
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return mutate(context.WithValue(ctx, mutationContextKey{}, r))
}

// WithCatalogDirectoryMutation additionally releases the handles for the
// affected source root. Other roots keep collecting changes while publication
// is serialized. Even failed mutations reconcile the resumed root.
func (r *RuntimeCatalogReloader) WithCatalogDirectoryMutation(ctx context.Context, reason string, mutate func(context.Context) error) error {
	return r.WithCatalogMutation(ctx, func(ctx context.Context) (err error) {
		if r.background != nil {
			for _, group := range r.background.groups {
				if !group.hasReason(reason) {
					continue
				}
				resume, suspendErr := group.watcher.Suspend()
				if suspendErr != nil {
					return suspendErr
				}
				defer func() {
					if resumeErr := resume(); resumeErr != nil {
						err = errors.Join(err, fmt.Errorf("restore %s catalog watcher: %w", reason, resumeErr))
					}
				}()
			}
		}
		return mutate(ctx)
	})
}
