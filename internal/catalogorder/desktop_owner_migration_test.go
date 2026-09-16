package catalogorder

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDesktopPinsCopyDoesNotOverrideCurrentUser(t *testing.T) {
	root := t.TempDir()
	store := NewFileOrderStore(root)
	store.SetPinned("user:app", "example", true)
	subject := "desktop-user:" + strings.Repeat("a", 64)
	backup := filepath.Join(t.TempDir(), "pins.json")
	copied, err := CopyLegacyDesktopPins(root, backup, subject)
	if err != nil || !copied {
		t.Fatal(copied, err)
	}
	state, err := store.Read("user:" + subject)
	if err != nil || len(state.Order) != 1 || state.Order[0] != "example" {
		t.Fatal(state, err)
	}
	store.SetPinned("user:"+subject, "example", false)
	copied, err = CopyLegacyDesktopPins(root, backup, subject)
	if err != nil || copied {
		t.Fatal("overwrote explicitly cleared pins", err)
	}
	state, err = store.Read("user:" + subject)
	if err != nil || len(state.Order) != 0 {
		t.Fatal(state, err)
	}
}
