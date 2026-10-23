package stream

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/view"
)

func TestDispatcherBindsMarkdownViewsOnCloseAndPersistsThemOnSnapshot(t *testing.T) {
	var calls []string
	binder := func(ref view.Reference, _ string, data json.RawMessage) (view.Reference, error) {
		calls = append(calls, ref.ConnectorID+"/"+ref.Key+" "+string(data))
		if ref.Key == "missing" {
			return view.Reference{}, view.ErrNotFound
		}
		ref.Hash = "h-" + ref.Key
		return ref, nil
	}
	d := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	events := d.Dispatch(ContentDelta{ContentID: "c1", Delta: "Here:\n```view key=crm/orders\n{\"items\":[]}\n``", BindView: binder})
	assertEventTypes(t, events, "content.start", "content.delta")
	// The closing fence binds only once its line is terminated.
	events = d.Dispatch(ContentDelta{ContentID: "c1", Delta: "`\nnext\n```view key=crm/missing\n{}\n```", BindView: binder})
	assertEventTypes(t, events, "content.delta", "content.view")
	if events[1].Payload["index"] != 0 || events[1].Payload["view"].(map[string]any)["hash"] != "h-orders" || events[1].Payload["viewError"] != nil {
		t.Fatalf("first binding: %#v", events[1].Payload)
	}
	events = d.Dispatch(ToolArgs{ToolID: "tool_1", ToolName: "datetime", Delta: "{"})
	assertEventTypes(t, events, "content.view", "content.end", "content.snapshot", "tool.start", "tool.args")
	if events[0].Payload["index"] != 1 || events[0].Payload["viewError"] != "view_not_found" {
		t.Fatalf("final binding: %#v", events[0].Payload)
	}
	views, _ := events[2].Payload["views"].([]map[string]any)
	if len(views) != 2 || views[0]["index"] != 0 || views[1]["viewError"] != "view_not_found" {
		t.Fatalf("snapshot views: %#v", events[2].Payload["views"])
	}
	if len(calls) != 2 || calls[0] != `crm/orders {"items":[]}` {
		t.Fatalf("binder calls: %#v", calls)
	}
}

func TestDispatcherReportsInvalidBlocksWithoutCallingBinder(t *testing.T) {
	called := false
	binder := func(ref view.Reference, _ string, data json.RawMessage) (view.Reference, error) {
		called = true
		return ref, nil
	}
	d := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	events := d.Dispatch(ContentDelta{ContentID: "c1", Delta: "```view key=card\n{}\n```\n```view key=a/b\n7\n```\n", BindView: binder})
	assertEventTypes(t, events, "content.start", "content.delta", "content.view", "content.view")
	if events[2].Payload["viewError"] != "invalid_view" || events[2].Payload["view"] != nil {
		t.Fatalf("header error: %#v", events[2].Payload)
	}
	if events[3].Payload["viewError"] != "view_data_invalid" || events[3].Payload["view"].(map[string]any)["key"] != "b" {
		t.Fatalf("data error: %#v", events[3].Payload)
	}
	if called {
		t.Fatal("binder called for invalid blocks")
	}
}

func TestDispatcherLeavesViewBlocksUnboundWithoutBinder(t *testing.T) {
	d := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	_ = d.Dispatch(ContentDelta{ContentID: "c1", Delta: "```view key=a/b\n{}\n```\n"})
	events := d.Dispatch(ToolArgs{ToolID: "tool_1", ToolName: "datetime", Delta: "{"})
	assertEventTypes(t, events, "content.end", "content.snapshot", "tool.start", "tool.args")
	if _, ok := events[1].Payload["views"]; ok {
		t.Fatalf("unexpected views: %#v", events[1].Payload)
	}
}

func TestDispatcherDiscardDropsContentViewBindings(t *testing.T) {
	binder := func(ref view.Reference, _ string, data json.RawMessage) (view.Reference, error) { return ref, nil }
	d := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	_ = d.Dispatch(ContentDelta{ContentID: "c1", Delta: "```view key=a/b\n{}\n```\n", BindView: binder})
	_ = d.Dispatch(ModelTurnDiscard{ContentIDs: []string{"c1"}})
	if len(d.state.contentViews) != 0 || len(d.state.contentBinders) != 0 {
		t.Fatalf("bindings kept after discard: %#v %#v", d.state.contentViews, d.state.contentBinders)
	}
}
