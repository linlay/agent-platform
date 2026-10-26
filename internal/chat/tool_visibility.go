package chat

import (
	"strings"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/stream"
)

// ClientHiddenToolFunc reports tools declared clientVisible=false in the
// current tool registry, the same source the live stream reads at admission.
type ClientHiddenToolFunc func(toolName string) bool

// hides also covers the session-local Team delegate tool, which never appears
// in the shared registry.
func (f ClientHiddenToolFunc) hides(toolName string) bool {
	return agentteam.IsHiddenTool(toolName) || (f != nil && f(strings.TrimSpace(toolName)))
}

// SetClientHiddenTools binds history replay and search to the tool registry.
func (s *FileStore) SetClientHiddenTools(hidden ClientHiddenToolFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hiddenTools = hidden
}

// SetClientHiddenTools binds archived history replay to the tool registry.
func (s *ArchiveStore) SetClientHiddenTools(hidden ClientHiddenToolFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hiddenTools = hidden
}

// dropClientHiddenToolEvents applies the live normalizer's rule to replayed
// history: every tool.* event of a hidden tool is removed, at any detail level.
// Results and failed tool.end lines carry only toolId, so ids are collected
// from the whole history first. Awaiting, source, and activity events stay.
func dropClientHiddenToolEvents(events []stream.EventData, hidden ClientHiddenToolFunc) []stream.EventData {
	hiddenIDs := map[string]bool{}
	for _, event := range events {
		if strings.HasPrefix(event.Type, "tool.") && hidden.hides(event.String("toolName")) {
			if toolID := event.String("toolId"); toolID != "" {
				hiddenIDs[toolID] = true
			}
		}
	}
	kept := events[:0]
	for _, event := range events {
		if strings.HasPrefix(event.Type, "tool.") &&
			(hidden.hides(event.String("toolName")) || hiddenIDs[event.String("toolId")]) {
			continue
		}
		kept = append(kept, event)
	}
	return kept
}
