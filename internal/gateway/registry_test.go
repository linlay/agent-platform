package gateway

import "testing"

func TestChannelFromChatID(t *testing.T) {
	cases := map[string]string{
		"wecom#single#user1#abc":    "wecom",
		"feishu#p2p#ou_xxx#def":     "feishu",
		"ding#group#conv#ghi":       "ding",
		"":                          "",
		"noPrefix":                  "",
		"#leading":                  "",
		"wecom#group#team#site#zz":  "wecom",
	}
	for in, want := range cases {
		if got := channelFromChatID(in); got != want {
			t.Errorf("channelFromChatID(%q) = %q, want %q", in, got, want)
		}
	}
}

// LookupByChatID 的路由语义用直接构造 entries 的 Registry 验证，
// 不走 Register（避免启真 ws）。
func TestLookupByChatIDRouting(t *testing.T) {
	r := &Registry{
		entries:   map[string]*Entry{},
		byChannel: map[string]string{},
	}
	r.entries["wecom-xiaozhai"] = &Entry{ID: "wecom-xiaozhai", Channel: "wecom"}
	r.byChannel["wecom"] = "wecom-xiaozhai"
	r.entries["feishu-bot1"] = &Entry{ID: "feishu-bot1", Channel: "feishu"}
	r.byChannel["feishu"] = "feishu-bot1"

	// wecom# 前缀 → wecom-xiaozhai
	if e, ok := r.LookupByChatID("wecom#single#u1#1"); !ok || e.ID != "wecom-xiaozhai" {
		t.Fatalf("wecom routing failed: ok=%v entry=%+v", ok, e)
	}
	// feishu# 前缀 → feishu-bot1
	if e, ok := r.LookupByChatID("feishu#p2p#ou#1"); !ok || e.ID != "feishu-bot1" {
		t.Fatalf("feishu routing failed: ok=%v entry=%+v", ok, e)
	}
	// 未知前缀 + 多条 entry → 不命中
	if _, ok := r.LookupByChatID("ding#x#y#z"); ok {
		t.Fatalf("unknown channel with multiple entries should miss")
	}
}

func TestLookupByChatIDLegacySingleGatewayFallback(t *testing.T) {
	// 单 gateway、channel 空：任何 chatId 都应路由到它（legacy 兼容）
	r := &Registry{
		entries:   map[string]*Entry{},
		byChannel: map[string]string{},
	}
	r.entries["default"] = &Entry{ID: "default", Channel: ""}

	for _, chatID := range []string{"wecom#x#y#z", "feishu#x#y#z", "anything", ""} {
		e, ok := r.LookupByChatID(chatID)
		if !ok || e.ID != "default" {
			t.Errorf("legacy fallback failed for chatId=%q: ok=%v entry=%+v", chatID, ok, e)
		}
	}
}
