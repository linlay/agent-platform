package session

import (
	"context"
	"encoding/json"
	"log"
	"strings"
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
	session.ViewCatalogPrompt = BuildViewCatalogPrompt(def, mounts, session.PromptAppend.View)
	service := s.ViewService()
	chatDir := session.ChatRoot
	if s.deps.Chats != nil {
		chatDir = s.deps.Chats.ChatDir(session.ChatID)
	}
	// The closure is created separately for each member session and uses its
	// frozen mounts, even when its public owner is a Team.
	var mu sync.Mutex
	cache := map[string]view.Reference{}
	resolve := func(ctx context.Context, ref view.Reference, usage string) (view.Reference, error) {
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
	session.ResolveView = resolve
	session.BindMarkdownView = func(ctx context.Context, ref view.Reference, query string, body json.RawMessage) (view.Reference, error) {
		resolved, err := resolve(ctx, ref, "display")
		if err != nil {
			return view.Reference{}, err
		}
		// The template is frozen even when the body fails, so the client can
		// show the error next to the exact view the model addressed.
		definition := mountDefinition(mounts, ref)
		if query != "" {
			_, err = view.ValidateQuery(definition.Queries, query, body)
			return resolved, err
		}
		return resolved, view.ValidateData(definition, body)
	}
	return nil
}

func mountDefinition(mounts []view.Mount, ref view.Reference) view.Definition {
	for _, mount := range mounts {
		if mount.ID == ref.ConnectorID {
			return mount.Views[ref.Key]
		}
	}
	return view.Definition{}
}

// BuildViewCatalogPrompt lists the display views of the executor's frozen
// mounts. All wording comes from agent-prompt.yml; without configured rules,
// an empty catalog, or a non-native backend the section is omitted.
func BuildViewCatalogPrompt(def catalog.AgentDefinition, mounts []view.Mount, config contracts.ViewAppendConfig) string {
	instructions := strings.TrimSpace(config.InstructionsPrompt)
	header := strings.TrimSpace(config.CatalogHeader)
	if instructions == "" || header == "" || def.Engine == catalog.AgentEngineACP || IsProxyRoutedAgent(def) {
		return ""
	}
	entries, omitted := view.RenderCatalog(mounts)
	if entries == "" {
		return ""
	}
	if omitted > 0 {
		log.Printf("[server][view-catalog][warn] agent %s: %d views omitted, catalog exceeds %d bytes", def.Key, omitted, view.MaxCatalogBytes)
	}
	return instructions + "\n\n" + header + "\n\n" + entries
}
