package reload

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCatalogMutationExcludesBackgroundReloadAndAllowsExplicitReload(t *testing.T) {
	r := NewRuntimeCatalogReloader(nil, nil, nil, nil, "", nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- r.WithCatalogMutation(context.Background(), func(ctx context.Context) error {
			// Reentrant publication is part of the transaction, not a second lock.
			if err := r.Reload(ctx, "skills"); err != nil {
				return err
			}
			close(entered)
			<-release
			return errors.New("injected mutation failure")
		})
	}()
	<-entered
	background := make(chan error, 1)
	go func() { background <- r.Reload(context.Background(), "skills") }()
	select {
	case <-background:
		t.Fatal("background reload entered unfinished mutation")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("lost mutation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mutation deadlocked")
	}
	select {
	case err := <-background:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload lock was not released")
	}
}
