package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/coder/acp-go-sdk"
)

const maxFrameBytes = 1 << 20

// Prompt resources are limited to 10 MiB before encoding. JSON text escaping
// can expand them sixfold (base64 only four thirds); leave room for the message,
// skill disclosures and RPC envelope. Incoming frames retain their own limit.
const maxPromptFrameBytes = 64 << 20

func outgoingFrameLimit(method string) int {
	if method == "session/prompt" {
		return maxPromptFrameBytes
	}
	return maxFrameBytes
}

type frame struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  json.RawMessage   `json:"params,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *sdk.RequestError `json:"error,omitempty"`
}
type transport struct {
	notificationCount, notificationBytes int
	done                                 chan struct{}
	mu                                   sync.Mutex
	inflight                             map[string]context.CancelFunc
	maxIncoming                          int
	p                                    *Process
	handler                              Handler
	responses                            *io.PipeWriter
	readResponses                        *io.PipeReader
	writeMu                              sync.Mutex
	promptWritten                        map[string]chan struct{}
	ctx                                  context.Context
	cancel                               context.CancelFunc
}

func (p *Process) connect(handler Handler, options Options) {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	wire := &transport{done: make(chan struct{}), promptWritten: map[string]chan struct{}{}, inflight: map[string]context.CancelFunc{}, maxIncoming: options.MaxIncoming, p: p, handler: handler, responses: writer, readResponses: reader, ctx: ctx, cancel: cancel}
	p.wire = wire
	// Responses and notifications retain the SDK's ordering barrier. Requests are
	// intercepted so the host keeps their original IDs, which MethodHandler omits.
	p.conn = sdk.NewConnection(func(ctx context.Context, method string, params json.RawMessage) (any, *sdk.RequestError) {
		defer func() {
			wire.mu.Lock()
			wire.notificationCount--
			wire.notificationBytes -= len(method) + len(params)
			wire.mu.Unlock()
		}()
		if handler == nil {
			return nil, nil
		}
		ctx, cancel := context.WithTimeout(ctx, p.host.options.RequestTimeout)
		defer cancel()
		stopClosed := context.AfterFunc(wire.ctx, cancel)
		defer stopClosed()
		if wire.ctx.Err() != nil {
			return nil, nil
		}
		stopTimeout := context.AfterFunc(ctx, func() {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				p.stop()
			}
		})
		defer stopTimeout()
		return handler(ctx, Incoming{Generation: p.generation, Method: method, Params: params})
	}, wire, reader)
	p.conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	go wire.read()
}
func (t *transport) Write(data []byte) (int, error) {
	var sent struct {
		Method string
		Params struct {
			SessionID string `json:"sessionId"`
		}
	}
	isPrompt := json.Unmarshal(data, &sent) == nil && sent.Method == "session/prompt"
	if len(data) > outgoingFrameLimit(sent.Method) {
		t.p.stop()
		return 0, errors.New("engine_frame_too_large")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	n, err := t.p.stdin.Write(data)
	if isPrompt {
		if written := t.promptWritten[sent.Params.SessionID]; written != nil {
			close(written)
			delete(t.promptWritten, sent.Params.SessionID)
		}
	}
	return n, err
}
func (t *transport) close() { t.cancel(); t.responses.Close(); t.readResponses.Close() }
func (t *transport) read() {
	defer t.p.stop()
	defer close(t.done)
	defer t.responses.Close()
	scanner := bufio.NewScanner(t.p.stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		data := append([]byte(nil), scanner.Bytes()...)
		m, err := decodeFrame(data)
		if err != nil {
			t.p.stop()
			return
		}
		if m.Method != "" && len(m.ID) > 0 {
			key, err := requestID(m.ID)
			if err != nil {
				t.p.stop()
				return
			}
			t.mu.Lock()
			_, duplicate := t.inflight[key]
			if duplicate || len(t.inflight) >= t.maxIncoming {
				t.mu.Unlock()
				t.p.stop()
				return
			}
			ctx, cancel := context.WithTimeout(t.ctx, t.p.host.options.RequestTimeout)
			t.inflight[key] = cancel
			t.mu.Unlock()
			go func() {
				stopTimeout := context.AfterFunc(ctx, func() {
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						t.p.stop()
					}
				})
				defer func() { stopTimeout(); cancel(); t.mu.Lock(); delete(t.inflight, key); t.mu.Unlock() }()
				t.request(ctx, m)
			}()
			continue
		}
		if m.Method == "$/cancel_request" {
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &params) != nil {
				t.p.stop()
				return
			}
			key, err := requestID(params.RequestID)
			if err != nil {
				t.p.stop()
				return
			}
			t.mu.Lock()
			cancel := t.inflight[key]
			t.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			continue
		}
		if m.Method != "" {
			t.mu.Lock()
			t.notificationCount++
			t.notificationBytes += len(m.Method) + len(m.Params)
			overflow := t.notificationCount > 32 || t.notificationBytes > 4*maxFrameBytes
			t.mu.Unlock()
			if overflow {
				t.p.stop()
				return
			}
		}
		if _, err := t.responses.Write(append(data, '\n')); err != nil {
			return
		}
	}
}
func (t *transport) request(ctx context.Context, m frame) {
	if ctx.Err() != nil {
		return
	}
	var result any
	var rpcErr *sdk.RequestError
	if t.handler == nil {
		rpcErr = sdk.NewMethodNotFound(m.Method)
	} else {
		result, rpcErr = t.handler(ctx, Incoming{Generation: t.p.generation, ID: m.ID, Method: m.Method, Params: m.Params})
	}
	if ctx.Err() != nil {
		return
	}
	response := frame{JSONRPC: "2.0", ID: m.ID, Error: rpcErr}
	if rpcErr == nil {
		data, err := json.Marshal(result)
		if err != nil {
			response.Error = sdk.NewInternalError(nil)
		} else {
			response.Result = data
		}
	}
	data, err := json.Marshal(response)
	if err == nil {
		_, err = t.Write(append(data, '\n'))
	}
	if err != nil {
		t.p.stop()
	}
}

// Bound numeric normalization before allocating big integers; preserve the wire
// ID in replies, using the canonical key only for duplicate/cancel lookup.
func requestID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", errors.New("invalid rpc id")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "", errors.New("invalid rpc id")
	}
	switch id := value.(type) {
	case string:
		return "s:" + id, nil
	case json.Number:
		text := string(id)
		if len(text) > 128 {
			return "", errors.New("invalid rpc id")
		}
		if index := strings.IndexAny(text, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(text[index+1:])
			if err != nil || exponent > 1000 || exponent < -1000 {
				return "", errors.New("invalid rpc id")
			}
		}
		number, ok := new(big.Rat).SetString(text)
		if ok {
			return "n:" + number.RatString(), nil
		}
	}
	return "", errors.New("invalid rpc id")
}

func decodeFrame(data []byte) (frame, error) {
	var m frame
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return m, errors.New("invalid rpc frame")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return m, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return m, errors.New("duplicate rpc field")
		}
		seen[key] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return m, err
		}
	}
	if json.Unmarshal(data, &m) != nil || m.JSONRPC != "2.0" {
		return m, errors.New("invalid rpc frame")
	}
	if len(m.ID) > 0 {
		if _, err := requestID(m.ID); err != nil {
			return m, err
		}
	}
	if m.Method != "" {
		if seen["result"] || seen["error"] {
			return m, errors.New("invalid rpc request")
		}
	} else if len(m.ID) == 0 || seen["result"] == seen["error"] || seen["params"] || seen["error"] && m.Error == nil {
		return m, errors.New("invalid rpc response")
	}
	return m, nil
}
