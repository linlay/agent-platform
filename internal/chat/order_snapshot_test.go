package chat

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestChatOrderSnapshotConcurrentPins(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range []string{"first", "second"} {
		if _, _, err := store.EnsureChat(id, "agent", "", id); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			if _, _, err := store.SetChatPinned("first", i%2 == 0); err != nil {
				t.Error(err)
				return
			}
			if _, _, err := store.SetChatPinned("second", i%3 == 0); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		snapshot, err := store.ChatOrderSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(snapshot.Chats))
		for _, item := range snapshot.Chats {
			if !item.Pinned {
				t.Fatal("snapshot contains an unpinned item")
			}
			ids = append(ids, item.ChatID)
		}
		if !reflect.DeepEqual(ids, snapshot.Pins.Order) {
			t.Fatal("snapshot membership and order diverged")
		}
	}
	workers.Wait()
	if err := os.WriteFile(filepath.Join(store.root, ChatPinnedFileName), []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChatOrderSnapshot(); err == nil {
		t.Fatal("invalid pins must fail, not become an empty snapshot")
	}
}
