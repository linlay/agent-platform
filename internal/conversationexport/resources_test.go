package conversationexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/stream"
)

func TestSnapshotExportDiscoversOnlyVisibleCurrentChatMedia(t *testing.T) {
	const id = "share-resources"
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	htmlBody := []byte(strings.Repeat(" ", 4096) + `<img src="@chat/图.svg"><img src="photo.svg"><img srcset="@chat/图.svg 1x, @chat/missing.png 2x"><video poster="@chat/图.svg" src="https://example.invalid/video.mp4"></video><script src="@chat/local.js"></script><style>p{background:url(@chat/css.png)}</style>`)
	store := resourceStore(t, id, map[string][]byte{
		"图.svg": image, "holiday photo.svg": image, "artifacts/run/photo.svg": image,
		"unreferenced.svg": image, "input-only.svg": image, "artifacts/run/page.html": htmlBody,
	})
	hash := sha256.Sum256(htmlBody)
	if err := store.AppendArtifactManifest(id, "run", testEpoch, []map[string]any{{"artifactId": "page", "type": "file", "name": "page.html", "mimeType": "text/html", "url": "artifacts/run/page.html", "sizeBytes": len(htmlBody), "sha256": hex.EncodeToString(hash[:])}}); err != nil {
		t.Fatal(err)
	}
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "references": []map[string]any{{"type": "file", "url": "@chat/图.svg", "meta": map[string]any{"token": "private"}}}}},
		{Type: "artifact.publish", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "artifacts": []map[string]any{{"name": "legacy.html", "url": "artifacts/run/legacy.html"}}}},
		{Type: "request.steer", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run", "message": "adjust", "references": []map[string]any{{"type": "file", "url": "missing.png"}}}},
		{Type: "tool.result", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "run", "toolId": "image", "toolName": "image_generate", "arguments": `{"images":["input-only.svg"]}`, "result": map[string]any{"images": []map[string]any{{"url": "%E5%9B%BE.svg", "path": "/private/source"}}}}},
		{Type: "content.snapshot", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "run", "text": "![same](@chat/图.svg) ![space](@chat/holiday photo.svg) `![hidden](unreferenced.svg)`"}},
	}
	counted := &manifestReadCounter{FileStore: store}
	summary := &chat.Summary{ChatID: id, CreatedAt: testEpoch}
	doc, err := BuildDocument(context.Background(), summary, events, testEpoch+5, "zh-CN", nil, chatresource.NewService(counted).Export(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Snapshot.Attachments) != 4 || len(doc.Snapshot.OmittedResources) != 1 {
		t.Fatalf("attachments=%+v omitted=%+v", doc.Snapshot.Attachments, doc.Snapshot.OmittedResources)
	}
	media := doc.Snapshot.Attachments[1]
	missing := doc.Snapshot.OmittedResources[0]
	if media.SourceRef != "%E5%9B%BE.svg" || media.Size != int64(len(image)) || media.SHA256 != "" || missing.Reason != "not_found" {
		t.Fatalf("media=%+v missing=%+v", media, missing)
	}
	raw, _ := json.Marshal(media)
	if strings.Contains(string(raw), "sha256") {
		t.Fatalf("candidate serialized empty hash: %s", raw)
	}
	if got := doc.Snapshot.Attachments[0].MediaRefs; got["@chat/图.svg"] != media.ID || got["@chat/missing.png"] != missing.ID || got["photo.svg"] != doc.Snapshot.Attachments[2].ID || len(got) != 3 {
		t.Fatalf("HTML refs=%v", got)
	}
	nodes := doc.Snapshot.Turns[0].Nodes
	for i, want := range [][]string{{media.ID}, {missing.ID}, {media.ID}, {media.ID, doc.Snapshot.Attachments[3].ID}} {
		if !reflect.DeepEqual(nodes[i].AttachmentIDs, want) {
			t.Fatalf("node %d refs=%v", i, nodes[i].AttachmentIDs)
		}
	}
	if bytes.Contains(doc.JSON, []byte(`"meta"`)) || bytes.Contains(doc.JSON, []byte(strings.Repeat(" ", 1024))) {
		t.Fatal("query metadata or HTML body leaked into JSON")
	}
	second, err := BuildDocument(context.Background(), summary, events, testEpoch+5, "zh-CN", nil, chatresource.NewService(counted).Export(id))
	if err != nil || !bytes.Equal(doc.JSON, second.JSON) || counted.reads != 2 {
		t.Fatalf("repeated exports differ or reread manifests: reads=%d err=%v", counted.reads, err)
	}
	// A same-size HTML edit must fail integrity before discovering dependencies.
	htmlBody[len(htmlBody)-1] = '!'
	if err := os.WriteFile(filepath.Join(store.ChatDir(id), "artifacts/run/page.html"), htmlBody, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := BuildDocument(context.Background(), summary, events, testEpoch+5, "zh-CN", nil, chatresource.NewService(store).Export(id))
	if err != nil || len(changed.Snapshot.Attachments) != 3 || len(changed.Snapshot.Attachments[0].MediaRefs) != 0 || len(changed.Snapshot.OmittedResources) != 1 {
		t.Fatalf("changed HTML: %+v err=%v", changed.Snapshot, err)
	}
}

func TestMarkdownMediaReferencesUseParsedNodes(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []string
	}{
		{name: "mixed syntax", source: "![a](image.png)\n![b][pic]\n\n[pic]: <@chat/图.svg>\n`![hidden](secret.png)`\n```html\n<img src=\"hidden.png\">\n```\n[video](clip.mp4)\nplain secret.png\n<img src=\"poster.png\">", want: []string{"image.png", "@chat/图.svg", "clip.mp4"}},
		{name: "unclosed code fence", source: "```md\n![hidden](private.png)"},
		{name: "tilde code fence", source: "~~~\n![hidden](private.png)\n~~~~"},
		{name: "indented code", source: "    ![hidden](private.png)"},
		{name: "tab code", source: "\t![hidden](private.png)"},
		{name: "code span with backtick", source: "`` ` ![hidden](private.png) ``"},
		{name: "raw html", source: "<img src=\"private.png\">"},
		{name: "space in local path", source: "![visible](@chat/holiday photo.svg)", want: []string{"@chat/holiday photo.svg"}},
		{name: "local space inside code", source: "`![hidden](@chat/holiday photo.svg)`"},
		{name: "space inside image alt", source: "![outer ![hidden](@chat/private photo.svg)](visible.svg)", want: []string{"visible.svg"}},
		{name: "image inside link", source: "[![visible](image.svg)](https://example.invalid)", want: []string{"image.svg"}},
		{name: "list continuation", source: "- item\n\n    ![visible](image.svg)", want: []string{"image.svg"}},
		{name: "quoted code", source: "> ~~~md\n> ![example](private.svg)\n> ~~~"},
		{name: "list code", source: "- ~~~md\n  ![example](private.svg)\n  ~~~"},
		{name: "image alt", source: "![outer ![inner](private.svg)](visible.svg)", want: []string{"visible.svg"}},
		{name: "escaped destination", source: `![visible](my\_image.svg)`, want: []string{"my_image.svg"}},
		{name: "entities", source: `![visible](a&amp;b.svg)`, want: []string{"a&b.svg"}},
		{name: "duplicate reference definition", source: "![visible][pic]\n\n[pic]: first.svg\n[pic]: other.svg", want: []string{"first.svg"}},
		{name: "definition cannot interrupt paragraph", source: "![example][pic]\n[pic]: private.svg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := markdownMediaRefs(tc.source); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("references=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestSnapshotExportScansOnlyRenderedMarkdown(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"query.svg", "steer.svg", "thinking.svg", "content.svg", "planning.svg", "tool.svg", "failed.svg", "code.svg"} {
		files[name] = []byte("<svg></svg>")
	}
	store := resourceStore(t, "markdown-scope", files)
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch, Payload: map[string]any{"runId": "run", "message": "![example](query.svg)"}},
		{Type: "request.steer", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "message": "![example](steer.svg)"}},
		{Type: "reasoning.snapshot", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run", "text": "![example](thinking.svg)"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "run", "text": "![visible](content.svg)"}},
		{Type: "planning.snapshot", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "run", "text": "![visible](planning.svg)"}},
		{Type: "tool.result", Timestamp: testEpoch + 5, Payload: map[string]any{"runId": "run", "toolId": "visible", "result": "![visible](tool.svg)"}},
		{Type: "tool.result", Timestamp: testEpoch + 6, Payload: map[string]any{"runId": "run", "toolId": "failed", "error": "failed", "result": "![example](failed.svg)"}},
		{Type: "tool.result", Timestamp: testEpoch + 7, Payload: map[string]any{"runId": "run", "toolId": "code", "result": map[string]any{"text": "![example](code.svg)"}}},
	}
	doc, err := BuildDocument(context.Background(), &chat.Summary{ChatID: "markdown-scope", CreatedAt: testEpoch}, events, testEpoch+8, "zh-CN", nil, chatresource.NewService(store).Export("markdown-scope"))
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, item := range doc.Snapshot.Attachments {
		refs = append(refs, item.SourceRef)
	}
	if !reflect.DeepEqual(refs, []string{"content.svg", "planning.svg", "tool.svg"}) || len(doc.Snapshot.OmittedResources) != 0 {
		t.Fatalf("references=%q omitted=%+v", refs, doc.Snapshot.OmittedResources)
	}
}

func TestSnapshotExportStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc, err := BuildDocument(ctx, &chat.Summary{ChatID: "cancelled", CreatedAt: testEpoch}, nil, testEpoch+1, "zh-CN", nil, nil)
	if !errors.Is(err, context.Canceled) || doc.JSON != nil {
		t.Fatalf("document=%+v err=%v", doc, err)
	}
}

func TestSnapshotExportDoesNotChangeOrdinaryProjection(t *testing.T) {
	const id = "projection-boundary"
	store := resourceStore(t, id, nil)
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "text", "message": "original"}},
		{Type: "request.steer", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "text", "message": "human correction"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "text", "text": "answer"}},
		{Type: "request.query", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "file", "references": []map[string]any{{"type": "file", "url": "missing.png"}}}},
		{Type: "content.snapshot", Timestamp: testEpoch + 5, Payload: map[string]any{"runId": "file", "text": "file answer"}},
	}
	events = append(events, stream.EventData{Type: "run.complete", Timestamp: testEpoch + 6, Payload: map[string]any{"runId": "text"}}, stream.EventData{Type: "run.complete", Timestamp: testEpoch + 7, Payload: map[string]any{"runId": "file"}})
	summary := &chat.Summary{ChatID: id, CreatedAt: testEpoch}
	ordinary, err := BuildSnapshot(summary, events, testEpoch+8, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Turns) != 1 || len(ordinary.Turns[0].Nodes) != 2 {
		t.Fatalf("ordinary projection changed: %+v", ordinary)
	}
	markdown, err := RenderMarkdown(ordinary)
	if err != nil || strings.Contains(string(markdown), "human correction") || strings.Contains(string(markdown), "file answer") {
		t.Fatalf("ordinary Markdown changed: %s; %v", markdown, err)
	}
	shared, err := BuildDocument(context.Background(), summary, events, testEpoch+8, "zh-CN", nil, chatresource.NewService(store).Export(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.Snapshot.Turns) != 2 || len(shared.Snapshot.Turns[0].Nodes) != 3 || len(shared.Snapshot.Turns[1].Nodes[0].AttachmentIDs) != 1 {
		t.Fatalf("share lost visible content: %s", shared.JSON)
	}
	if shared.Snapshot.Turns[0].Nodes[1].Text != "human correction" {
		t.Fatal("share lost human steer")
	}
}

func resourceStore(t *testing.T, id string, files map[string][]byte) *chat.FileStore {
	t.Helper()
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, _, err := store.EnsureChat(id, "agent", "media"); err != nil {
		t.Fatal(err)
	}
	for ref, body := range files {
		filename := filepath.Join(store.ChatDir(id), filepath.FromSlash(ref))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// Counts actual store reads, including HTML and unregistered sibling lookups.
type manifestReadCounter struct {
	*chat.FileStore
	reads int
}

func (s *manifestReadCounter) PublishedArtifacts(id string) ([]chat.ArtifactManifestItem, error) {
	s.reads++
	return s.FileStore.PublishedArtifacts(id)
}
