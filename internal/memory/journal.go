package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

func AppendJournal(root string, item api.StoredMemoryResponse) error {
	createdAt := item.CreatedAt
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	created := time.UnixMilli(createdAt)
	monthDir := filepath.Join(root, "journal", created.Format("2006-01"))
	if err := os.MkdirAll(monthDir, 0o755); err != nil {
		return err
	}
	journalPath := filepath.Join(monthDir, created.Format("2006-01-02")+".md")
	entry := buildJournalEntry(created, item)
	file, err := os.OpenFile(journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(entry)
	return err
}

func buildJournalEntry(created time.Time, item api.StoredMemoryResponse) string {
	var builder strings.Builder
	builder.WriteString("## ")
	builder.WriteString(created.Format(time.RFC3339))
	builder.WriteString("\n\n")
	builder.WriteString("- id: ")
	builder.WriteString(item.ID)
	builder.WriteString("\n")
	if item.ChatID != "" {
		builder.WriteString("- chatId: ")
		builder.WriteString(item.ChatID)
		builder.WriteString("\n")
	}
	if item.AgentKey != "" {
		builder.WriteString("- agentKey: ")
		builder.WriteString(item.AgentKey)
		builder.WriteString("\n")
	}
	if item.SubjectKey != "" {
		builder.WriteString("- subjectKey: ")
		builder.WriteString(item.SubjectKey)
		builder.WriteString("\n")
	}
	builder.WriteString("- sourceType: ")
	builder.WriteString(item.SourceType)
	builder.WriteString("\n")
	builder.WriteString("- category: ")
	builder.WriteString(item.Category)
	builder.WriteString("\n")
	builder.WriteString("- importance: ")
	builder.WriteString(fmt.Sprintf("%d", item.Importance))
	builder.WriteString("\n\n")
	builder.WriteString(item.Summary)
	builder.WriteString("\n\n")
	return builder.String()
}
