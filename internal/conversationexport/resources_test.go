package conversationexport

import (
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
	store := resourceStore(t, id)
	write := func(ref string, body []byte) {
		t.Helper()
		filename := filepath.Join(store.ChatDir(id), filepath.FromSlash(ref))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	write("图.svg", image)
	write("holiday photo.svg", image)
	write("artifacts/run/photo.svg", image)
	write("unreferenced.svg", image)
	write("input-only.svg", image)
	htmlBody := []byte(`<img src="@chat/图.svg"><img src="photo.svg"><img srcset="@chat/图.svg 1x, @chat/missing.png 2x"><video poster="@chat/图.svg" src="https://example.invalid/video.mp4"></video><script src="@chat/local.js"></script><style>p{background:url(@chat/css.png)}</style>`)
	write("artifacts/run/page.html", htmlBody)
	hash := sha256.Sum256(htmlBody)
	if err := store.AppendArtifactManifest(id, "run", testEpoch, []map[string]any{{"artifactId": "page", "type": "file", "name": "page.html", "mimeType": "text/html", "url": "artifacts/run/page.html", "sizeBytes": len(htmlBody), "sha256": hex.EncodeToString(hash[:])}}); err != nil {
		t.Fatal(err)
	}
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "references": []map[string]any{{"type": "file", "url": "@chat/图.svg", "meta": map[string]any{"token": "private"}}}}},
		{Type: "request.steer", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run", "message": "adjust", "references": []map[string]any{{"type": "file", "url": "missing.png"}}}},
		{Type: "tool.result", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "run", "toolId": "image", "toolName": "image_generate", "arguments": `{"images":["input-only.svg"]}`, "result": map[string]any{"images": []map[string]any{{"url": "%E5%9B%BE.svg", "path": "/private/source"}}}}},
		{Type: "content.snapshot", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "run", "text": "![same](@chat/图.svg) ![space](@chat/holiday photo.svg) `![hidden](unreferenced.svg)`"}},
	}
	doc, err := resourceDocument(context.Background(), &chat.Summary{ChatID: id, CreatedAt: testEpoch}, events, store, testEpoch+5, "zh-CN", nil)
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
	if strings.Contains(string(doc.JSON), `"meta"`) {
		t.Fatal("query metadata leaked")
	}
}

func TestSnapshotResourceReadsRejectUnsafeAndUnsupportedFiles(t *testing.T) {
	store := resourceStore(t, "media")
	root := store.ChatDir("media")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("ordinary text"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.svg")
	if err := os.WriteFile(outside, []byte("<svg></svg>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.svg")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, reason string }{
		{"fake.png", "unsupported"}, {"escape.svg", "forbidden"}, {"missing.png", "not_found"}, {".tools/secret.png", "invalid"}, {".btw/secret.png", "invalid"},
		{"../other/a.png", "invalid"}, {"%252e%252e/a.png", "invalid"}, {"a%2Fb.png", "invalid"}, {"C:/Windows/a.png", "invalid"}, {`C:\Windows\a.png`, "invalid"}, {"https://example.test/a.png", "invalid"}, {"/tmp/a.png", "invalid"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			f, _, err := chatresource.NewService(store).OpenExportResource(context.Background(), "media", tc.ref)
			if f != nil {
				f.Close()
			}
			if err == nil || SnapshotResourceReason(err) != tc.reason {
				t.Fatalf("reason=%s err=%v", SnapshotResourceReason(err), err)
			}
		})
	}
}

func TestMarkdownMediaReferencesRespectSyntax(t *testing.T) {
	input := "![a](image.png)\n![b][pic]\n\n[pic]: <@chat/图.svg>\n`![hidden](secret.png)`\n```html\n<img src=\"hidden.png\">\n```\n[video](clip.mp4)\nplain secret.png\n<img src=\"poster.png\">"
	if got, want := markdownMediaRefs(input), []string{"image.png", "@chat/图.svg", "clip.mp4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestMarkdownMediaReferencesUseParsedNodes(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []string
	}{
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
	store := resourceStore(t, "markdown-scope")
	root := store.ChatDir("markdown-scope")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"query.svg", "steer.svg", "thinking.svg", "content.svg", "planning.svg", "tool.svg", "failed.svg", "code.svg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("<svg></svg>"), 0600); err != nil {
			t.Fatal(err)
		}
	}
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
	doc, err := resourceDocument(context.Background(), &chat.Summary{ChatID: "markdown-scope", CreatedAt: testEpoch}, events, store, testEpoch+8, "zh-CN", nil)
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

func TestSnapshotHTMLDiscoveryDoesNotApplySnapshotByteLimit(t *testing.T) {
	store := resourceStore(t, "html-budget")
	root := store.ChatDir("html-budget")
	if err := os.MkdirAll(filepath.Join(root, "artifacts/run"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("<!doctype html><html><body>" + strings.Repeat(" ", 4096) + `<img src="@chat/image.svg"></body></html>`)
	if err := os.WriteFile(filepath.Join(root, "artifacts/run/page.html"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.svg"), []byte("<svg></svg>"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if err := store.AppendArtifactManifest("html-budget", "run", testEpoch, []map[string]any{{"type": "file", "artifactId": "page", "name": "page.html", "mimeType": "text/html", "url": "artifacts/run/page.html", "sizeBytes": len(body), "sha256": hex.EncodeToString(hash[:])}}); err != nil {
		t.Fatal(err)
	}
	events := []stream.EventData{{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "run", "message": "HTML"}}}
	doc, err := resourceDocument(context.Background(), &chat.Summary{ChatID: "html-budget", CreatedAt: testEpoch}, events, store, testEpoch+2, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.JSON) >= 1024 || len(doc.Snapshot.Attachments) != 2 || len(doc.Snapshot.OmittedResources) != 0 {
		t.Fatalf("JSON bytes=%d attachments=%+v omitted=%+v", len(doc.JSON), doc.Snapshot.Attachments, doc.Snapshot.OmittedResources)
	}
	if doc.Snapshot.Attachments[0].MediaRefs["@chat/image.svg"] != doc.Snapshot.Attachments[1].ID {
		t.Fatalf("media mappings=%v", doc.Snapshot.Attachments[0].MediaRefs)
	}
	// A same-size edit must still fail integrity before discovering dependencies.
	body[len(body)-1] = '!'
	if err := os.WriteFile(filepath.Join(root, "artifacts/run/page.html"), body, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err = resourceDocument(context.Background(), &chat.Summary{ChatID: "html-budget", CreatedAt: testEpoch}, events, store, testEpoch+2, "zh-CN", nil)
	if err != nil || len(doc.Snapshot.Attachments) != 1 || len(doc.Snapshot.Attachments[0].MediaRefs) != 0 || len(doc.Snapshot.OmittedResources) != 0 {
		t.Fatalf("changed HTML: attachments=%+v omitted=%+v err=%v", doc.Snapshot.Attachments, doc.Snapshot.OmittedResources, err)
	}
	if f, _, err := chatresource.NewService(store).OpenExportResource(context.Background(), "html-budget", "artifacts/run/page.html"); !errors.Is(err, chatresource.ErrArtifactChanged) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("changed published HTML bypassed integrity: %v", err)
	}
}

func TestSnapshotExportStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc, err := resourceDocument(ctx, &chat.Summary{ChatID: "cancelled", CreatedAt: testEpoch}, []stream.EventData{{Type: "request.query", Timestamp: testEpoch, Payload: map[string]any{"runId": "run", "message": "cancel"}}}, nil, testEpoch+1, "zh-CN", nil)
	if !errors.Is(err, context.Canceled) || doc.JSON != nil {
		t.Fatalf("document=%+v err=%v", doc, err)
	}
}

func TestMarkdownMediaSkipsCodeExamples(t *testing.T) {
	for _, source := range []string{
		"```md\n![hidden](private.png)",
		"~~~\n![hidden](private.png)\n~~~~",
		"    ![hidden](private.png)",
		"\t![hidden](private.png)",
		"`` ` ![hidden](private.png) ``",
		"<img src=\"private.png\">",
	} {
		if got := markdownMediaRefs(source); len(got) != 0 {
			t.Errorf("%q: %v", source, got)
		}
	}
}

func TestSnapshotExportDoesNotChangeOrdinaryProjection(t *testing.T) {
	const id = "projection-boundary"
	store := resourceStore(t, id)
	events := []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"runId": "text", "message": "original"}},
		{Type: "request.steer", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "text", "message": "human correction"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 3, Payload: map[string]any{"runId": "text", "text": "answer"}},
		{Type: "request.query", Timestamp: testEpoch + 4, Payload: map[string]any{"runId": "file", "references": []map[string]any{{"type": "file", "url": "missing.png"}}}},
		{Type: "content.snapshot", Timestamp: testEpoch + 5, Payload: map[string]any{"runId": "file", "text": "file answer"}},
	}
	events = append(events, stream.EventData{Type: "run.complete", Timestamp: testEpoch + 6, Payload: map[string]any{"runId": "text"}}, stream.EventData{Type: "run.complete", Timestamp: testEpoch + 7, Payload: map[string]any{"runId": "file"}})
	summary := &chat.Summary{ChatID: id, CreatedAt: testEpoch}
	ordinary, err := snapshotDocument(summary, events, nil, testEpoch+8, "zh-CN", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Snapshot.Turns) != 1 || len(ordinary.Snapshot.Turns[0].Nodes) != 2 {
		t.Fatalf("ordinary projection changed: %s", ordinary.JSON)
	}
	markdown, err := RenderMarkdown(ordinary.Snapshot)
	if err != nil || strings.Contains(string(markdown), "human correction") || strings.Contains(string(markdown), "file answer") {
		t.Fatalf("ordinary Markdown changed: %s; %v", markdown, err)
	}
	shared, err := resourceDocument(context.Background(), summary, events, store, testEpoch+8, "zh-CN", nil)
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

func resourceStore(t *testing.T, id string) *chat.FileStore {
	t.Helper()
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, _, err := store.EnsureChat(id, "agent", "media"); err != nil {
		t.Fatal(err)
	}
	return store
}
