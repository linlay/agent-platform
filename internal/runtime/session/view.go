package session

import (
	"context"
	"sync"

	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/view"
)

func (s *Builder) ViewService() *view.Service {
	stateRoot := s.deps.Config.Paths.EffectiveConnectorStateDir()
	return &view.Service{ResolveHeaders: func(id string, headers map[string]string) (map[string]string, error) {
		return connector.ResolveViewHeaders(stateRoot, id, headers)
	}}
}

func MountedViews(def catalog.AgentDefinition) ([]view.Mount, error) {
	var mounts []view.Mount
	for _, mount := range def.ConnectorMounts {
		pkg, err := connector.LoadDirectory(mount.Dir, mount.ID)
		if err != nil {
			return nil, err
		}
		if len(pkg.Views) > 0 {
			mounts = append(mounts, pkg.ViewMount())
		}
	}
	return mounts, nil
}

func (s *Builder) ConfigureViews(session *contracts.QuerySession, def catalog.AgentDefinition) error {
	mounts, err := MountedViews(def)
	if err != nil {
		return err
	}
	service := s.ViewService()
	chatDir := session.ChatRoot
	if s.deps.Chats != nil {
		chatDir = s.deps.Chats.ChatDir(session.ChatID)
	}
	// The closure is created separately for each member session and uses its
	// frozen mounts, even when its public owner is a Team.
	var mu sync.Mutex
	cache := map[string]view.Reference{}
	session.ResolveView = func(ctx context.Context, ref view.Reference, usage string) (view.Reference, error) {
		if ref.ConnectorID == "" {
			resolved, err := view.ResolveBuiltin(ref.Key)
			if err != nil {
				return view.Reference{}, err
			}
			return *resolved, nil
		}
		mu.Lock()
		defer mu.Unlock()
		key := ref.ConnectorID + "\x00" + ref.Key + "\x00" + usage
		if existing, ok := cache[key]; ok {
			return existing, nil
		}
		doc, err := service.Resolve(ctx, mounts, ref, usage)
		if err != nil {
			return view.Reference{}, err
		}
		resolved, err := view.SaveSnapshot(chatDir, doc)
		if err == nil {
			cache[key] = resolved
		}
		return resolved, err
	}
	return nil
}
