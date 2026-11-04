package voice

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/models"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type ASRClient struct {
	socket    *dashScopeSocket
	silenceMS int
}

func NewASRClient(model models.ModelDefinition, provider models.ProviderDefinition, silenceMS int) (*ASRClient, error) {
	if silenceMS < 200 || silenceMS > 6000 {
		return nil, errors.New("invalid voice sentence silence")
	}
	socket, err := newDashScopeSocket(model, provider, models.ModelTypeASR, models.ProtocolDashScopeASR)
	if err != nil {
		return nil, err
	}
	return &ASRClient{socket: socket, silenceMS: silenceMS}, nil
}

func (c *ASRClient) Close() { c.socket.close() }

// Recognize consumes realtime PCM. Closing audio is manual stop; auto drains
// already accepted frames on its first sentence final. Only task-finished makes
// the returned transcript final. partial is never a Query trigger.
func (c *ASRClient) Recognize(ctx context.Context, audio <-chan []byte, mode string, partial func(string, bool)) (text string, err error) {
	if cap(audio)*640 > maxStartupPCM {
		return "", errors.New("voice startup audio overflow")
	}
	if mode != "auto" && mode != "manual" {
		return "", errors.New("invalid voice listening mode")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	id := uuid.NewString()
	// Audio stays buffered in the channel until task-started.
	conn, end, err := c.socket.start(ctx, id, map[string]any{"task_group": "audio", "task": "asr", "function": "recognition", "model": c.socket.model.ModelID, "input": map[string]any{}, "parameters": map[string]any{"format": "pcm", "sample_rate": 16000, "semantic_punctuation_enabled": false, "max_sentence_silence": c.silenceMS}})
	if err != nil {
		return "", err
	}
	success := false
	defer func() { end(success) }()
	type result struct {
		event *dashScopeEvent
		err   error
	}
	events := make(chan result, 8)
	readDone := make(chan struct{})
	readCtx, stopReader := context.WithCancel(ctx)
	go func() {
		defer close(readDone)
		for {
			// Silence is valid while recording. The task context bounds recording;
			// finishTimer below bounds the response after finish-task.
			_, event, err := dashRead(conn, id, time.Time{})
			select {
			case events <- result{event, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil || event != nil && event.Header.Event == "task-finished" {
				return
			}
		}
	}()
	defer func() {
		stopReader()
		if !success {
			_ = conn.Close()
		}
		<-readDone
	}()
	finishing := false
	totalBytes := 0
	input := audio
	finals := map[int]string{}
	finishedSentences := map[int]bool{}
	transcript := func() string {
		keys := make([]int, 0, len(finals))
		for key := range finals {
			keys = append(keys, key)
		}
		sort.Ints(keys)
		var out strings.Builder
		for _, key := range keys {
			out.WriteString(finals[key])
		}
		return out.String()
	}
	sendPCM := func(pcm []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
			return errors.New("voice provider audio write failed")
		}
		return nil
	}
	accept := func(pcm []byte) error {
		if len(pcm) == 0 || len(pcm) > 640 || len(pcm)%2 != 0 {
			return errors.New("invalid voice PCM frame")
		}
		totalBytes += len(pcm)
		if totalBytes > maxRecordingPCM {
			return errors.New("voice recording limit exceeded")
		}
		return sendPCM(pcm)
	}
	var finishTimer *time.Timer
	var finishedBy <-chan time.Time
	defer func() {
		if finishTimer != nil {
			finishTimer.Stop()
		}
	}()
	finish := func() error {
		if finishing {
			return nil
		}
		finishTimer = time.NewTimer(finishTimeout)
		finishedBy = finishTimer.C
		finishing = true
		input = nil
		return dashCommand(conn, id, "finish-task", map[string]any{"input": map[string]any{}})
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-finishedBy:
			return "", errors.New("voice recognition finish timeout")
		case pcm, ok := <-input:
			if !ok {
				input = nil
				if err = finish(); err != nil {
					return "", err
				}
			} else if err = accept(pcm); err != nil {
				return "", err
			}
		case result := <-events:
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if result.err != nil {
				return "", result.err
			}
			event := result.event
			if event == nil {
				return "", errors.New("unexpected ASR binary message")
			}
			switch event.Header.Event {
			case "task-started":
				return "", errors.New("duplicate voice task-started")
			case "result-generated":
				sentence := event.Payload.Output.Sentence
				if sentence.Heartbeat || strings.TrimSpace(sentence.Text) == "" || finishedSentences[sentence.Begin] {
					continue
				}
				finals[sentence.Begin] = sentence.Text
				current := transcript()
				if utf8.RuneCountInString(current) > maxTranscriptChars {
					return "", errors.New("voice transcript limit exceeded")
				}
				if partial != nil {
					partial(current, sentence.Final && mode == "auto")
				}
				if sentence.Final {
					finishedSentences[sentence.Begin] = true
					if mode == "auto" && !finishing {
						// Only frames already buffered at this boundary belong to this utterance.
						for n := len(input); n > 0; n-- {
							pcm, ok := <-input
							if !ok {
								break
							}
							if err = accept(pcm); err != nil {
								return "", err
							}
						}
						if err = finish(); err != nil {
							return "", err
						}
					}
				}
			case "task-finished":
				if !finishing {
					return "", errors.New("premature voice task-finished")
				}
				for key := range finals {
					if !finishedSentences[key] {
						delete(finals, key)
					}
				}
				success = true
				return transcript(), nil
			}
		}
	}
}
