package ws

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestRequestFrameDetail(t *testing.T) {
	for _, tc := range []struct {
		frame   RequestFrame
		level   stream.DetailLevel
		invalid bool
	}{
		{RequestFrame{Type: "/api/query"}, "", false},
		{RequestFrame{Type: "/api/attach", Detail: "full"}, stream.DetailFull, true},
		{RequestFrame{Type: "/api/chat", Detail: "summary"}, stream.DetailSummary, false},
		{RequestFrame{Type: "/api/query", Detail: "verbose"}, "", true},
		{RequestFrame{Type: "/api/query", Detail: "none"}, "", true},
		{RequestFrame{Type: "/api/chats", Detail: "full"}, stream.DetailFull, true},
	} {
		if got := tc.frame.DetailLevel(); !tc.invalid && got != tc.level {
			t.Fatalf("%#v level=%q want %q", tc.frame, got, tc.level)
		}
		if invalid := tc.frame.detailError() != ""; invalid != tc.invalid {
			t.Fatalf("%#v invalid=%v want %v", tc.frame, invalid, tc.invalid)
		}
	}
}
