package voice

import (
	"context"
	"errors"
	"time"
)

func (s *session) speak(t *turn, text string, hint bool) error {
	if t.speech != nil {
		t.speech.cancel()
	}
	ctx, cancel := context.WithCancel(t.ctx)
	media := &speechTask{ctx: ctx, cancel: cancel, hint: hint}
	t.speech = media
	if !hint {
		t.phase = "speaking"
	}
	if err := s.write(map[string]any{"type": "tts", "turn": t.number, "state": "start", "text": text}); err != nil {
		cancel()
		return err
	}
	s.service.start(func() {
		frameBytes := s.sampleRate * 2 / 50
		var tail []byte
		var first time.Time
		sent := 0
		emit := func(frame []byte) error {
			if first.IsZero() {
				first = time.Now()
			}
			sent += len(frame)
			target := first.Add(time.Duration(sent)*time.Second/time.Duration(s.sampleRate*2) - time.Duration(s.service.deps.Config.DownlinkLeadMS)*time.Millisecond)
			if delay := time.Until(target); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			ack := make(chan error, 1)
			if !s.post(ctx, sessionEvent{kind: "pcm", turn: t, speech: media, pcm: append([]byte(nil), frame...), ack: ack}) {
				return context.Canceled
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-ack:
				return err
			}
		}
		err := s.tts.Synthesize(ctx, text, s.voice, s.sampleRate, func(pcm []byte) error {
			for len(pcm) > 0 {
				n := min(frameBytes-len(tail), len(pcm))
				tail = append(tail, pcm[:n]...)
				pcm = pcm[n:]
				if len(tail) == frameBytes {
					if err := emit(tail); err != nil {
						return err
					}
					tail = tail[:0]
				}
			}
			return nil
		})
		if err == nil && len(tail)%2 != 0 {
			err = errors.New("unaligned PCM tail")
		}
		if err == nil && len(tail) > 0 {
			err = emit(tail)
		}
		s.post(ctx, sessionEvent{kind: "spoken", turn: t, speech: media, err: err})
	})
	return nil
}
