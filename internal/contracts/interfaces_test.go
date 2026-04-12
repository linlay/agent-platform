package contracts

import (
	"context"
	"errors"
	"testing"
)

func TestNoopToolExecutorReturnsErrNotImplemented(t *testing.T) {
	result, err := NewNoopToolExecutor().Invoke(context.Background(), "demo_tool", map[string]any{"value": 1}, nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if result.Error != "not_implemented" || result.ExitCode != -1 {
		t.Fatalf("expected not_implemented payload, got %#v", result)
	}
}

func TestNoopActionInvokerReturnsErrNotImplemented(t *testing.T) {
	result, err := NewNoopActionInvoker().Invoke(context.Background(), "demo_action", map[string]any{"value": 1}, nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if result.Error != "not_implemented" || result.ExitCode != -1 {
		t.Fatalf("expected not_implemented payload, got %#v", result)
	}
}

func TestNoopSandboxClientReturnsErrNotImplemented(t *testing.T) {
	result, err := NewNoopSandboxClient().Execute(context.Background(), nil, "pwd", "/tmp", 1000)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
	if result.ExitCode != -1 || result.Stderr != "status: not_implemented" {
		t.Fatalf("expected not_implemented payload, got %#v", result)
	}
}
