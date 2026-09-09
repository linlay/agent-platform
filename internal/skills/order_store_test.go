package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestSkillOrderPersistsPerUserAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	store := NewFileOrderStore(root)
	empty, err := store.Read("alice")
	if err != nil || empty.Order == nil || len(empty.Order) != 0 {
		t.Fatalf("empty: %#v %v", empty, err)
	}
	if _, err := store.SetPinned("alice", "PDF", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPinned("alice", "slides", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPinned("bob", "image", true); err != nil {
		t.Fatal(err)
	}
	restarted := NewFileOrderStore(root)
	state, err := restarted.Read("alice")
	if err != nil || !reflect.DeepEqual(state.Order, []string{"slides", "pdf"}) {
		t.Fatalf("restored: %#v %v", state, err)
	}
	duplicate, err := restarted.SetPinned("alice", "pdf", true)
	if err != nil || !reflect.DeepEqual(duplicate, state) {
		t.Fatalf("idempotent: %#v %v", duplicate, err)
	}
	state, err = restarted.SetPinned("alice", "pdf", false)
	if err != nil || !reflect.DeepEqual(state.Order, []string{"slides"}) {
		t.Fatalf("unpin: %#v %v", state, err)
	}
	bob, err := restarted.Read("bob")
	if err != nil || !reflect.DeepEqual(bob.Order, []string{"image"}) {
		t.Fatalf("isolation: %#v %v", bob, err)
	}
	if _, err := os.Stat(filepath.Join(root, "order.json")); err != nil {
		t.Fatal(err)
	}
}

func TestSkillOrderConcurrentPinsDoNotLoseChanges(t *testing.T) {
	store := NewFileOrderStore(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := store.SetPinned("alice", fmt.Sprintf("skill-%d", i), true); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	state, err := store.Read("alice")
	if err != nil || len(state.Order) != 20 {
		t.Fatalf("concurrent pins: %#v %v", state, err)
	}
}

func TestSkillOrderRejectsCorruptFileWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, OrderFileName)
	for _, content := range []string{`bad-json`, `{"version":2,"users":{}}`, `{"version":1,"users":{"alice":{"order":["pdf","pdf"],"updatedAt":1}}}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileOrderStore(root).SetPinned("alice", "slides", true); err == nil {
			t.Fatal("corrupt file accepted")
		}
		actual, _ := os.ReadFile(path)
		if string(actual) != content {
			t.Fatal("corrupt file overwritten")
		}
	}
}
