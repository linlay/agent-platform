package conversationexport

import (
	"context"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/stream"
)

func snapshotDocument(summary *chat.Summary, events []stream.EventData, attachments []AttachmentV1, capturedAt int64, locale string, resolve ResolveAssistant) (SnapshotDocument, error) {
	snapshot, err := BuildSnapshot(summary, events, capturedAt, locale, resolve, false)
	if err != nil {
		return SnapshotDocument{}, err
	}
	if attachments != nil {
		snapshot.Attachments = attachments
	}
	return BuildExportDocument(context.Background(), snapshot, nil, nil)
}

func resourceDocument(ctx context.Context, summary *chat.Summary, events []stream.EventData, store chat.Store, capturedAt int64, locale string, resolve ResolveAssistant) (SnapshotDocument, error) {
	snapshot, err := BuildSnapshot(summary, events, capturedAt, locale, resolve, true)
	if err != nil {
		return SnapshotDocument{}, err
	}
	if err := ctx.Err(); err != nil {
		return SnapshotDocument{}, err
	}
	resources := chatresource.NewService(store)
	items, err := resources.PublishedArtifacts(summary.ChatID)
	if err != nil {
		return SnapshotDocument{}, err
	}
	return BuildExportDocument(ctx, snapshot, resources, items)
}
