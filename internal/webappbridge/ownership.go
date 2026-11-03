package webappbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"agent-platform/internal/runtime/types"
	"agent-platform/internal/webapp"
)

// The ledger lives in host management storage, outside application writable
// data and immutable package trees. Reinstallation gets a new namespace.
type ownership struct {
	mu   sync.Mutex
	root string
}
type ownedChat struct {
	ChatID   string `json:"chatId"`
	RunID    string `json:"runId"`
	AgentKey string `json:"agentKey"`
}
type ledger struct {
	Version      int         `json:"version"`
	Installation string      `json:"installationId"`
	Chats        []ownedChat `json:"chats"`
}

func (o *ownership) read(scope *webapp.Scope) (ledger, error) {
	l := ledger{Version: 1, Installation: scope.InstallationID, Chats: []ownedChat{}}
	if scope.InstallationID == "" || filepath.Base(scope.InstallationID) != scope.InstallationID {
		return l, sdkError("app_grant_required", 403)
	}
	path := filepath.Join(o.root, scope.InstallationID+".json")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return l, sdkError("ownership_unavailable", 503)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return l, sdkError("ownership_unavailable", 503)
	}
	raw := make([]byte, info.Size())
	if _, err = f.ReadAt(raw, 0); err != nil {
		return l, sdkError("ownership_unavailable", 503)
	}
	if webapp.DecodeRequest(raw, &l) != nil || l.Version != 1 || l.Installation != scope.InstallationID || len(l.Chats) > 1024 {
		return l, sdkError("ownership_unavailable", 503)
	}
	return l, nil
}
func (o *ownership) claim(scope *webapp.Scope, ref types.RunRef) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := scope.Check(); err != nil {
		return err
	}
	l, err := o.read(scope)
	if err != nil {
		return err
	}
	for _, c := range l.Chats {
		if c.ChatID == ref.ChatID {
			if c.RunID == ref.RunID && c.AgentKey == ref.AgentKey {
				return nil
			}
			return sdkError("app_grant_required", 403)
		}
	}
	if len(l.Chats) >= 1024 {
		return sdkError("app_capacity_exceeded", 429)
	}
	l.Chats = append(l.Chats, ownedChat{ref.ChatID, ref.RunID, ref.AgentKey})
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(o.root, 0700); err != nil {
		return sdkError("ownership_unavailable", 503)
	}
	f, err := os.CreateTemp(o.root, ".claim-*")
	if err != nil {
		return sdkError("ownership_unavailable", 503)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(o.root, scope.InstallationID+".json"))
	}
	if err == nil {
		var d *os.File
		d, err = os.Open(o.root)
		if err == nil {
			err = d.Sync()
			d.Close()
		}
	}
	if err != nil {
		return sdkError("ownership_unavailable", 503)
	}
	return nil
}
func (o *ownership) get(scope *webapp.Scope, chatID, runID string) (ownedChat, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	l, err := o.read(scope)
	if err != nil {
		return ownedChat{}, err
	}
	for _, c := range l.Chats {
		if c.ChatID == chatID && (runID == "" || c.RunID == runID) {
			return c, nil
		}
	}
	return ownedChat{}, sdkError("app_grant_required", 403)
}
