package voice

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/models"
	"github.com/google/uuid"
)

// Playback is paced in real time, so the text cap keeps ordinary speech rates
// (about four Chinese characters per second) well inside the audio cap, and
// the task deadline outlasts the longest allowed playback.
const (
	maxSynthesisRunes = 900
	maxSynthesisAudio = 300 * time.Second
)

// CosyVoice bounds the audio of one provider segment and drops the excess while
// sentence-end and task-finished still report the whole text. One continue-task
// carrying a whole reply becomes one segment (2026-11-04, cosyvoice-v3-flash:
// 155–255 rune replies all stopped at exactly 33.44s), so text is sent one
// sentence per continue-task and an overlong sentence is split at a pause. The
// cap matches the provider's own streaming segments; 80-rune unpunctuated
// lists already garbled words.
const maxSegmentRunes = 50

const (
	sentenceEnds    = "。！？；…!?;"
	sentenceClosers = "”’」』）)\"'"
	segmentPauses   = "，、：,:"
)

func speechSegments(text string) []string {
	runes := []rune(text)
	var segments []string
	add := func(r []rune) {
		if s := strings.TrimSpace(string(r)); s != "" {
			segments = append(segments, s)
		}
	}
	start, pause := 0, -1
	for i := 0; i < len(runes); i++ {
		end := -1
		r := runes[i]
		switch {
		case strings.ContainsRune(sentenceEnds, r) || r == '.' && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])):
			end = i + 1
			for end < len(runes) && strings.ContainsRune(sentenceEnds+sentenceClosers, runes[end]) {
				end++
			}
		case strings.ContainsRune(segmentPauses, r) || unicode.IsSpace(r):
			pause = i + 1
		}
		if end < 0 && i+1-start >= maxSegmentRunes {
			end = i + 1
			if pause > start {
				end = pause
			}
		}
		if end > 0 {
			add(runes[start:end])
			start, pause, i = end, -1, end-1
		}
	}
	add(runes[start:])
	return segments
}

type TTSClient struct{ socket *dashScopeSocket }

func NewTTSClient(model models.ModelDefinition, provider models.ProviderDefinition) (*TTSClient, error) {
	socket, err := newDashScopeSocket(model, provider, models.ModelTypeTTS, models.ProtocolDashScopeRealtimeTTS)
	if err != nil {
		return nil, err
	}
	return &TTSClient{socket: socket}, nil
}

func (c *TTSClient) Close() { c.socket.close() }

// Synthesize sends the complete committed text in one task, segment by
// segment. The callback consumes PCM synchronously, providing backpressure
// without buffering the whole response.
func (c *TTSClient) Synthesize(ctx context.Context, text, voice string, sampleRate int, audio func([]byte) error) (err error) {
	segments := speechSegments(text)
	if len(segments) == 0 || utf8.RuneCountInString(text) > maxSynthesisRunes || voice == "" || audio == nil || (sampleRate != 16000 && sampleRate != 24000) {
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
	for _, segment := range segments {
		if err = dashCommand(conn, id, "continue-task", map[string]any{"input": map[string]any{"text": segment}}); err != nil {
			return err
		}
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
