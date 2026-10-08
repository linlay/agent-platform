package llm

import (
	"context"
	"errors"

	"agent-platform/internal/view"
)

// Presentation failure never approves a pending action or changes tool results.
// Publish a stable error code, without upstream URLs, credentials or host paths.
func (s *llmRunStream) resolveView(value any, usage string) (*view.Reference, string) {
	ref, err := view.ParseReference(value)
	if err != nil {
		return nil, "invalid_view"
	}
	if ref == nil {
		return nil, ""
	}
	if ref.ConnectorID == "" {
		resolved, err := view.ResolveBuiltin(ref.Key)
		if err != nil {
			return ref, "view_not_found"
		}
		return resolved, ""
	}
	if s.session.ResolveView == nil {
		return ref, "view_unavailable"
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := s.session.ResolveView(ctx, *ref, usage)
	if err == nil {
		return &resolved, ""
	}
	if errors.Is(err, view.ErrNotFound) {
		return ref, "view_not_found"
	}
	if errors.Is(err, view.ErrInvalid) {
		return ref, "invalid_view"
	}
	return ref, "view_unavailable"
}
