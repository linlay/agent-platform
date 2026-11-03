package automation

import (
	"errors"
	"sync"
	"testing"
)

func TestExecutionStoreConcurrentCloseAndQueries(t *testing.T) {
	store, err := NewExecutionStore(t.TempDir(), "executions.db")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			check := func(err error) {
				if err != nil && !errors.Is(err, ErrExecutionHistoryUnavailable) {
					t.Error(err)
				}
			}
			for j := 0; j < 8; j++ {
				_, err := store.ListRunning()
				check(err)
				_, err = store.LastExecution("task")
				check(err)
				_, err = store.GetExecution("execution")
				check(err)
				_, _, err = store.ListByAutomation("task", 10, 0)
				check(err)
				_ = store.Status()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if status := store.Status(); status.Available {
		t.Fatal("closed store is still available")
	}
	if _, err := store.ListRunning(); !errors.Is(err, ErrExecutionHistoryUnavailable) {
		t.Fatalf("query after close: %v", err)
	}
}
