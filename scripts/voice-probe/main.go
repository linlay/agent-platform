// voice-probe performs a small, paid DashScope TTS -> ASR connectivity check.
// It reads the credential from a provider YAML and never writes it to artifacts.
// This is a standalone probe, not the Platform voice implementation.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"agent-platform/internal/config"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type event struct {
	Header struct {
		Event        string `json:"event"`
		TaskID       string `json:"task_id"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	} `json:"header"`
	Payload struct {
		Output struct {
			Sentence struct {
				Text      string `json:"text"`
				Begin     int    `json:"begin_time"`
				Final     bool   `json:"sentence_end"`
				Heartbeat bool   `json:"heartbeat"`
			} `json:"sentence"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	} `json:"payload"`
}

type probe struct {
	endpoint string
	header   http.Header
	deadline time.Time
}

func (p probe) dial(ctx context.Context) (*websocket.Conn, float64, error) {
	start := time.Now()
	// Explicit direct transport: no Transit Hub or process environment proxy.
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	c, response, err := d.DialContext(ctx, p.endpoint, p.header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			return nil, ms(start), fmt.Errorf("websocket handshake HTTP %d", response.StatusCode)
		}
		return nil, ms(start), err
	}
	c.SetReadLimit(4 << 20)
	_ = c.SetReadDeadline(p.deadline)
	_ = c.SetWriteDeadline(p.deadline)
	return c, ms(start), nil
}

func send(c *websocket.Conn, id, action string, payload any) error {
	return c.WriteJSON(map[string]any{"header": map[string]any{"action": action, "task_id": id, "streaming": "duplex"}, "payload": payload})
}

func read(c *websocket.Conn, id string) ([]byte, *event, error) {
	kind, b, err := c.ReadMessage()
	if err != nil {
		return nil, nil, err
	}
	if kind == websocket.BinaryMessage {
		return b, nil, nil
	}
	var e event
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, nil, fmt.Errorf("invalid provider JSON")
	}
	if e.Header.TaskID != id {
		return nil, nil, fmt.Errorf("unexpected task ID")
	}
	if e.Header.Event == "task-failed" {
		return nil, nil, fmt.Errorf("provider %s: %s", e.Header.ErrorCode, e.Header.ErrorMessage)
	}
	return nil, &e, nil
}

func (p probe) tts(ctx context.Context, model, voice, text string) ([]byte, map[string]any, error) {
	m := map[string]any{"model": model, "voice": voice, "sample_rate": 16000, "format": "pcm"}
	c, latency, err := p.dial(ctx)
	m["connect_ms"] = latency
	if err != nil {
		return nil, m, err
	}
	defer c.Close()
	id := uuid.NewString()
	start := time.Now()
	err = send(c, id, "run-task", map[string]any{"task_group": "audio", "task": "tts", "function": "SpeechSynthesizer", "model": model, "parameters": map[string]any{"text_type": "PlainText", "voice": voice, "format": "pcm", "sample_rate": 16000}, "input": map[string]any{}})
	if err != nil {
		return nil, m, err
	}
	_, e, err := read(c, id)
	if err != nil {
		return nil, m, err
	}
	if e == nil || e.Header.Event != "task-started" {
		return nil, m, fmt.Errorf("expected task-started")
	}
	m["task_started_ms"] = ms(start)
	textSent := time.Now()
	if err := send(c, id, "continue-task", map[string]any{"input": map[string]any{"text": text}}); err != nil {
		return nil, m, err
	}
	if err := send(c, id, "finish-task", map[string]any{"input": map[string]any{}}); err != nil {
		return nil, m, err
	}
	var audio []byte
	chunks := 0
	for {
		b, e, err := read(c, id)
		if err != nil {
			return nil, m, err
		}
		if len(b) > 0 {
			if chunks == 0 {
				m["first_audio_from_text_ms"] = ms(textSent)
				m["first_audio_from_run_task_ms"] = ms(start)
			}
			audio = append(audio, b...)
			chunks++
			if len(audio) > 32000*30 {
				return nil, m, fmt.Errorf("audio exceeds 30s probe limit")
			}
		}
		if e != nil && len(e.Payload.Usage) > 0 && string(e.Payload.Usage) != "null" {
			m["provider_usage"] = e.Payload.Usage
		}
		if e != nil && e.Header.Event == "task-finished" {
			break
		}
	}
	if len(audio) == 0 || len(audio)%2 != 0 || bytes.HasPrefix(audio, []byte("RIFF")) {
		return nil, m, fmt.Errorf("expected nonempty raw PCM16 audio")
	}
	peak := 0
	for i := 0; i < len(audio); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(audio[i:])))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return nil, m, fmt.Errorf("audio is entirely silent")
	}
	m["task_finished_ms"] = ms(start)
	m["audio_bytes"] = len(audio)
	m["audio_duration_ms"] = float64(len(audio)) / 32
	m["binary_messages"] = chunks
	m["pcm_peak"] = peak
	return audio, m, nil
}

func (p probe) asr(ctx context.Context, model string, audio []byte) (map[string]any, error) {
	m := map[string]any{"model": model, "sample_rate": 16000, "format": "pcm", "send_chunk_ms": 100, "trailing_silence_ms": 1200, "max_sentence_silence_ms": 600}
	c, latency, err := p.dial(ctx)
	m["connect_ms"] = latency
	if err != nil {
		return m, err
	}
	defer c.Close()
	id := uuid.NewString()
	start := time.Now()
	err = send(c, id, "run-task", map[string]any{"task_group": "audio", "task": "asr", "function": "recognition", "model": model, "parameters": map[string]any{"format": "pcm", "sample_rate": 16000, "semantic_punctuation_enabled": false, "max_sentence_silence": 600, "language_hints": []string{"zh"}}, "input": map[string]any{}})
	if err != nil {
		return m, err
	}
	_, e, err := read(c, id)
	if err != nil {
		return m, err
	}
	if e == nil || e.Header.Event != "task-started" {
		return m, fmt.Errorf("expected task-started")
	}
	m["task_started_ms"] = ms(start)
	type sent struct {
		end, finish time.Time
		err         error
	}
	sentCh := make(chan sent, 1)
	streamStart := time.Now()
	go func() {
		all := append(append([]byte(nil), audio...), make([]byte, 38400)...)
		var end time.Time
		for offset := 0; offset < len(all); offset += 3200 {
			n := min(offset+3200, len(all))
			if err := c.WriteMessage(websocket.BinaryMessage, all[offset:n]); err != nil {
				sentCh <- sent{err: err}
				c.Close()
				return
			}
			if offset < len(audio) && n >= len(audio) {
				end = time.Now().Add(time.Duration(len(audio)-offset) * time.Second / 32000)
			}
			target := streamStart.Add(time.Duration(n) * time.Second / 32000)
			timer := time.NewTimer(max(time.Until(target), 0))
			select {
			case <-ctx.Done():
				timer.Stop()
				sentCh <- sent{err: ctx.Err()}
				c.Close()
				return
			case <-timer.C:
			}
		}
		finish := time.Now()
		err := send(c, id, "finish-task", map[string]any{"input": map[string]any{}})
		sentCh <- sent{end: end, finish: finish, err: err}
		if err != nil {
			c.Close()
		}
	}()
	var finals []string
	seen := map[int]bool{}
	var firstFinal time.Time
	for {
		_, e, err := read(c, id)
		if err != nil {
			return m, err
		}
		if e == nil {
			continue
		}
		if len(e.Payload.Usage) > 0 && string(e.Payload.Usage) != "null" {
			m["provider_usage"] = e.Payload.Usage
		}
		if e.Header.Event == "result-generated" {
			s := e.Payload.Output.Sentence
			if !s.Heartbeat && s.Text != "" {
				if _, ok := m["first_text_from_audio_ms"]; !ok {
					m["first_text_from_audio_ms"] = ms(streamStart)
				}
				if s.Final && !seen[s.Begin] {
					seen[s.Begin] = true
					finals = append(finals, s.Text)
					if firstFinal.IsZero() {
						firstFinal = time.Now()
					}
				}
			}
		}
		if e.Header.Event == "task-finished" {
			break
		}
	}
	s := <-sentCh
	if s.err != nil {
		return m, s.err
	}
	m["task_finished_ms"] = ms(start)
	m["text"] = strings.Join(finals, "")
	m["final_sentences"] = len(finals)
	if !firstFinal.IsZero() {
		m["first_final_from_audio_ms"] = float64(firstFinal.Sub(streamStart).Microseconds()) / 1000
		m["first_final_after_source_audio_ms"] = float64(firstFinal.Sub(s.end).Microseconds()) / 1000
		m["first_final_before_finish_task"] = firstFinal.Before(s.finish)
	}
	if len(finals) == 0 {
		return m, fmt.Errorf("no final recognition text")
	}
	return m, nil
}

func ms(start time.Time) float64 { return float64(time.Since(start).Microseconds()) / 1000 }
func normalized(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func main() {
	provider := flag.String("provider", "", "existing provider YAML path")
	out := flag.String("out", "", "directory for sanitized report and audio")
	ttsModel := flag.String("tts", "cosyvoice-v3-flash", "TTS model ID")
	asrModel := flag.String("asr", "paraformer-realtime-v2", "ASR model ID")
	voice := flag.String("voice", "longanyang", "TTS voice")
	flag.Parse()
	if *provider == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "-provider and -out are required")
		os.Exit(2)
	}
	tree, err := config.LoadYAMLTree(*provider)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unable to parse provider YAML")
		os.Exit(2)
	}
	values, _ := tree.(map[string]any)
	key, _ := values["apiKey"].(string)
	base, _ := values["baseUrl"].(string)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(key) == "" {
		fmt.Fprintln(os.Stderr, "provider requires an HTTPS baseUrl and nonempty apiKey")
		os.Exit(2)
	}
	u.Scheme = "wss"
	u.Path = strings.TrimRight(u.Path, "/") + "/api-ws/v1/inference"
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	p := probe{endpoint: u.String(), header: http.Header{"Authorization": []string{"Bearer " + strings.TrimSpace(key)}}, deadline: deadline}
	text := "你好，今天天气怎么样"
	report := map[string]any{"tested_at": time.Now().Format(time.RFC3339), "endpoint": u.String(), "route": "direct; no relay or environment proxy", "text": text, "provider_file": *provider, "scope": "one cold TTS task followed by one real-time-paced ASR task; no Agent Run"}
	audio, tts, err := p.tts(ctx, *ttsModel, *voice, text)
	report["tts"] = tts
	if err != nil {
		report["error"] = strings.ReplaceAll(err.Error(), key, "[REDACTED]")
	} else {
		if err = os.MkdirAll(*out, 0700); err == nil {
			err = os.WriteFile(filepath.Join(*out, "tts.pcm"), audio, 0600)
		}
		if err == nil {
			err = writeWAV(filepath.Join(*out, "tts.wav"), audio)
		}
		if err != nil {
			report["error"] = "unable to save probe audio"
		} else {
			asr, asrErr := p.asr(ctx, *asrModel, audio)
			report["asr"] = asr
			if asrErr != nil {
				report["error"] = strings.ReplaceAll(asrErr.Error(), key, "[REDACTED]")
			} else {
				report["normalized_text_matches"] = normalized(asr["text"].(string)) == normalized(text)
			}
		}
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	b = append(b, '\n')
	if err := os.MkdirAll(*out, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "unable to create output directory")
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*out, "report.json"), b, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "unable to save report")
		os.Exit(1)
	}
	fmt.Print(string(b))
	if _, failed := report["error"]; failed {
		os.Exit(1)
	}
}

func writeWAV(path string, pcm []byte) error {
	b := new(bytes.Buffer)
	b.WriteString("RIFF")
	_ = binary.Write(b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return os.WriteFile(path, b.Bytes(), 0600)
}
