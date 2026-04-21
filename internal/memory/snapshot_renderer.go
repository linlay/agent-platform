package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform-runner-go/internal/api"
)

func refreshSnapshots(root string, agentKey string, items []api.StoredMemoryResponse) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(agentKey) == "" {
		return nil
	}
	agentRoot := filepath.Join(root, agentKey)
	snapshotDir := filepath.Join(agentRoot, "snapshot")
	exportsDir := filepath.Join(agentRoot, "exports")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(exportsDir, 0o755); err != nil {
		return err
	}

	buckets := map[string][]api.StoredMemoryResponse{
		ScopeUser:   {},
		ScopeAgent:  {},
		ScopeTeam:   {},
		ScopeGlobal: {},
	}
	observations := make([]api.StoredMemoryResponse, 0)
	todos := make([]api.StoredMemoryResponse, 0)

	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if strings.TrimSpace(item.AgentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if item.Kind == KindObservation {
			if item.Status == StatusOpen || item.Status == StatusActive {
				observations = append(observations, item)
			}
			if item.Category == "todo" {
				todos = append(todos, item)
			}
			continue
		}
		scope := normalizeScopeType(item.ScopeType)
		if _, ok := buckets[scope]; ok && item.Status == StatusActive {
			buckets[scope] = append(buckets[scope], item)
		}
	}

	files := map[string]string{
		filepath.Join(snapshotDir, "USER.md"):               renderSnapshotFile("USER", buckets[ScopeUser]),
		filepath.Join(snapshotDir, "PROJECT.md"):            renderSnapshotFile("PROJECT", buckets[ScopeAgent]),
		filepath.Join(snapshotDir, "AGENT.md"):              renderSnapshotFile("AGENT", buckets[ScopeAgent]),
		filepath.Join(snapshotDir, "TEAM.md"):               renderSnapshotFile("TEAM", buckets[ScopeTeam]),
		filepath.Join(snapshotDir, "GLOBAL.md"):             renderSnapshotFile("GLOBAL", buckets[ScopeGlobal]),
		filepath.Join(exportsDir, "recent-observations.md"): renderObservationsFile(observations),
		filepath.Join(exportsDir, "open-todos.md"):          renderObservationsFile(todos),
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func renderSnapshotFile(label string, items []api.StoredMemoryResponse) string {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Importance != items[j].Importance {
			return items[i].Importance > items[j].Importance
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	lines := []string{"# " + label}
	if len(items) == 0 {
		lines = append(lines, "", "No active memory.")
		return strings.Join(lines, "\n")
	}
	for _, item := range items {
		lines = append(lines, "", "- "+sanitizeMemoryText(memoryLine(item)))
	}
	return strings.Join(lines, "\n")
}

func renderObservationsFile(items []api.StoredMemoryResponse) string {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	lines := []string{"# Export"}
	if len(items) == 0 {
		lines = append(lines, "", "No entries.")
		return strings.Join(lines, "\n")
	}
	for _, item := range items {
		lines = append(lines, "", fmt.Sprintf("- %s", sanitizeMemoryText(memoryLine(item))))
	}
	return strings.Join(lines, "\n")
}
