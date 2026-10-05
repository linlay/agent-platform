package kbx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerIsPerLibraryAndSubscriberDisconnectDoesNotStopIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan string, 3)
	release := make(chan struct{})
	var calls atomic.Int32
	w := NewWorkers(ctx, func(ctx context.Context, library string) error {
		calls.Add(1)
		entered <- library
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer w.Close(context.Background())
	for _, key := range []string{"a", "a", "b"} {
		if err := w.Start(key); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("different libraries must run concurrently")
		}
	}
	waiting, detach := context.WithCancel(ctx)
	detach()
	if err := w.Wait(waiting, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error=%v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("started %d connections", calls.Load())
	}
	if err := w.Start("a"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := w.Wait(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("subscriber disconnect restarted update")
	}
}
func TestWorkerReconnectUsesSameLibrary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	disconnected := errors.New("disconnected")
	var calls atomic.Int32
	w := NewWorkers(ctx, func(_ context.Context, key string) error {
		if key != "canonical-library" {
			t.Errorf("unexpected key: %s", key)
		}
		if calls.Add(1) == 1 {
			return disconnected
		}
		return nil
	})
	defer w.Close(context.Background())
	if err := w.Start("canonical-library"); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(ctx, "canonical-library"); !errors.Is(err, disconnected) {
		t.Fatalf("error=%v", err)
	}
	// Reading the final connection result must not launch a new update.
	_ = w.Wait(ctx, "canonical-library")
	if calls.Load() != 1 {
		t.Fatal("wait reconnected implicitly")
	}
	if err := w.Start("canonical-library"); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(ctx, "canonical-library"); err != nil {
		t.Fatal(err)
	}
}
func TestCloseCancelsConnectionsAndRejectsNewOnes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w := NewWorkers(ctx, func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() })
	if err := w.Start("a"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if err := w.Start("a"); err == nil {
		t.Fatal("started after close")
	}
}
