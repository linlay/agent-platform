package speech

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"agent-platform/internal/models"
	"github.com/google/uuid"
)

// Playback is paced in real time, so the text cap keeps ordinary speech rates
// (about four Chinese characters per second) well inside the audio cap, and
// the task deadline outlasts the longest allowed playback.
const (
	MaxSynthesisRunes = 900
	maxSynthesisAudio = 300 * time.Second
)

type TTSClient struct{ socket *dashScopeSocket }

func NewTTSClient(model models.ModelDefinition, provider models.ProviderDefinition) (*TTSClient, error) {
	socket, err := newDashScopeSocket(model, provider, models.ModelTypeTTS, models.ProtocolDashScopeRealtimeTTS)
	if err != nil {
		return nil, err
	}
	return &TTSClient{socket: socket}, nil
}

func (c *TTSClient) Close() { c.socket.close() }

// Synthesize sends the complete committed text once. The callback consumes PCM
// synchronously, providing backpressure without buffering the whole response.
func (c *TTSClient) Synthesize(ctx context.Context, text, voice string, sampleRate int, audio func([]byte) error) (err error) {
	if text == "" || utf8.RuneCountInString(text) > MaxSynthesisRunes || voice == "" || audio == nil || (sampleRate != 16000 && sampleRate != 24000) {
		return errors.New("invalid voice synthesis input")
	}
	ctx, cancel := context.WithTimeout(ctx, maxSynthesisAudio+time.Minute)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	id := uuid.NewString()
	conn, end, err := c.socket.start(ctx, id, map[string]any{"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer", "model": c.socket.model.ModelID, "input": map[string]any{}, "parameters": map[string]any{"text_type": "PlainText", "voice": voice, "format": "pcm", "sample_rate": sampleRate}})
	if err != nil {
		return err
	}
	success := false
	defer func() { end(success) }()
	if err = dashCommand(conn, id, "continue-task", map[string]any{"input": map[string]any{"text": text}}); err != nil {
		return err
	}
	if err = dashCommand(conn, id, "finish-task", map[string]any{"input": map[string]any{}}); err != nil {
		return err
	}
	bytes := 0
	for {
		pcm, event, readErr := dashRead(conn, id, time.Now().Add(finishTimeout))
		if readErr != nil {
			return readErr
		}
		if len(pcm) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			bytes += len(pcm)
			if bytes > sampleRate*2*int(maxSynthesisAudio/time.Second) {
				return errors.New("voice synthesis exceeds duration limit")
			}
			if err = audio(pcm); err != nil {
				return err
			}
		}
		if event != nil && event.Header.Event == "task-finished" {
			if bytes == 0 || bytes%2 != 0 {
				return errors.New("voice provider returned empty or unaligned PCM")
			}
			success = true
			return nil
		}
	}
}
