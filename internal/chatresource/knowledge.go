package chatresource

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/stream"
)

// PublishedKnowledgeSource resolves only persisted source cards from the owned
// Chat. Transports must authorize Chat access before invoking this method.
func (s *Service) PublishedKnowledgeSource(chatID, sourceID string) (stream.Source, error) {
	if s == nil || s.chats == nil {
		return stream.Source{}, ErrNotConfigured
	}
	detail, err := s.chats.LoadChat(chatID)
	if err != nil {
		return stream.Source{}, err
	}
	for _, event := range detail.Events {
		if event.Type != "source.publish" {
			continue
		}
		raw, err := json.Marshal(event.Value("sources"))
		if err != nil {
			continue
		}
		var sources []stream.Source
		if json.Unmarshal(raw, &sources) != nil {
			continue
		}
		for _, source := range sources {
			if source.ID == sourceID && source.LibraryID != "" && source.AgentKey != "" && strings.HasPrefix(source.ID, "kbase:"+source.LibraryID+"/") && len(source.Chunks) > 0 {
				return source, nil
			}
		}
	}
	return stream.Source{}, fmt.Errorf("published knowledge source not found; historical snippets remain available")
}
