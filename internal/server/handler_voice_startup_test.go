package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestVoiceSlowASRStartupPreservesBufferedAudio(t *testing.T) {
	f := newTestFixture(t)
	srv, provider := enableVoiceFixture(t, f, nil)
	provider.startDelay.Store(int64(3 * time.Second))
	provider.transcript.Store("")
	c, chatID := dialVoice(t, srv, "", 16000)
	if err := c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "manual"}); err != nil {
		t.Fatal(err)
	}
	// Record 2.5 seconds while ASR startup is delayed for 3 seconds. The old
	// 100-frame queue overflowed before the provider could consume any audio.
	const frames = 125
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < frames; i++ {
		<-ticker.C
		if err := c.WriteMessage(websocket.BinaryMessage, make([]byte, 640)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.WriteJSON(map[string]any{"type": "listen", "state": "stop", "mode": "manual"}); err != nil {
		t.Fatal(err)
	}
	msg, _ := voiceUntil(t, c, "stt", "")
	if msg["final"] != true || msg["text"] != "" || provider.pcmBytes.Load() != frames*640 {
		data, _ := json.Marshal(msg)
		t.Fatalf("buffered audio lost: final=%s PCM bytes=%d", data, provider.pcmBytes.Load())
	}
	if summary, err := f.chats.Summary(chatID); err != nil || summary != nil {
		t.Fatal("silent audio created a Chat")
	}
}
