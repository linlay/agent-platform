package automation

import (
	"context"
	"time"
)

func (s *ExecutionHistoryService) WaitReady(ctx context.Context) error {
	if s == nil {
		return ErrExecutionHistoryUnavailable
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.Status().Available {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
