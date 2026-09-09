package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"syscall"
	"time"
)

type Factory struct {
	resolver  *Resolver
	transport *Transport
}

func NewFactory(c Config) (*Factory, error) {
	r, err := NewResolver(c)
	if err != nil {
		return nil, err
	}
	return factoryForResolver(r), nil
}

func factoryForResolver(r *Resolver) *Factory {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		if d, ok := req.Context().Value(decisionKey{}).(Decision); ok {
			return d.Proxy, nil
		}
		return r.ProxyForRequest(req)
	}
	return &Factory{resolver: r, transport: &Transport{base: t, resolver: r}}
}

func (f *Factory) NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: f.transport, Timeout: timeout}
}

func (f *Factory) Refresh()              { f.resolver.Refresh() }
func (f *Factory) CloseIdleConnections() { f.transport.CloseIdleConnections() }

var processFactory atomic.Pointer[Factory]
var directFactory = func() *Factory { f, _ := NewFactory(Config{Mode: "direct"}); return f }()

func init() { f, _ := NewFactory(Config{}); processFactory.Store(f) }

// ConfigureDefault is called once during Platform startup, before constructing
// business clients. Existing clients retain their factory and configuration.
func ConfigureDefault(c Config) error {
	f, err := NewFactory(c)
	if err != nil {
		return err
	}
	old := processFactory.Swap(f)
	if old != nil {
		old.CloseIdleConnections()
	}
	return nil
}

func NewClient(timeout time.Duration) *http.Client    { return processFactory.Load().NewClient(timeout) }
func DirectClient(timeout time.Duration) *http.Client { return directFactory.NewClient(timeout) }
func DefaultTransport() http.RoundTripper             { return processFactory.Load().transport }

// CloneTransport preserves proxy routing and diagnostics when MCP customizes
// its per-server connect timeout. Unknown injected RoundTrippers stay intact.
func CloneTransport(base http.RoundTripper, connectTimeout time.Duration) http.RoundTripper {
	if base == nil {
		base = DefaultTransport()
	}
	var raw *http.Transport
	switch t := base.(type) {
	case *Transport:
		raw = t.base.Clone()
	case *http.Transport:
		raw = t.Clone()
	default:
		return base
	}
	if connectTimeout > 0 {
		raw.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	if t, ok := base.(*Transport); ok {
		return &Transport{base: raw, resolver: t.resolver}
	}
	return raw
}

type decisionKey struct{}

type Transport struct {
	base     *http.Transport
	resolver *Resolver
}

func (t *Transport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	d, err := t.resolver.Resolve(req.Context(), req.URL)
	if err != nil {
		return nil, reportFailure(d, "proxy-resolution", err)
	}
	slog.Debug("outbound HTTP route", "source", d.Source, "proxy", safeProxy(d.Proxy))
	var phase atomic.Int32
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) { phase.Store(1) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				phase.Store(2)
			}
		},
		GotConn: func(httptrace.GotConnInfo) { phase.Store(3) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				phase.Store(4)
			}
		},
	}
	ctx := context.WithValue(req.Context(), decisionKey{}, d)
	response, err := t.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(ctx, trace)))
	if err != nil {
		stage := "connection-acquire"
		switch phase.Load() {
		case 1:
			stage = "connect"
		case 2:
			stage = "tls"
		case 3:
			stage = "request-write"
		case 4:
			stage = "response-headers"
		}
		if d.Proxy != nil {
			if phase.Load() <= 1 {
				stage = "proxy-connect"
			}
			if phase.Load() == 2 {
				stage = "proxy-tunnel-or-tls"
			}
		}
		return nil, reportFailure(d, stage, err)
	}
	if d.Proxy != nil && response.StatusCode == http.StatusProxyAuthRequired {
		log.Printf("outbound HTTP failed source=%s proxy=%s stage=proxy-auth status=407", d.Source, safeProxy(d.Proxy))
	}
	if response.Body != nil {
		response.Body = &diagnosticBody{ReadCloser: response.Body, decision: d}
	}
	return response, nil
}

type diagnosticBody struct {
	io.ReadCloser
	decision Decision
}

func (b *diagnosticBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		return n, reportFailure(b.decision, "response-body", err)
	}
	return n, err
}

type TransportError struct {
	Source, Proxy, Stage string
	cause                error
}

func (e *TransportError) Error() string {
	detail := failureKind(e.cause)
	if e.Stage == "proxy-resolution" {
		detail = e.cause.Error()
	}
	return fmt.Sprintf("outbound HTTP source=%s proxy=%s stage=%s: %s", e.Source, e.Proxy, e.Stage, detail)
}
func (e *TransportError) Unwrap() error { return e.cause }
func (e *TransportError) Timeout() bool {
	var n net.Error
	return errors.As(e.cause, &n) && n.Timeout()
}

func reportFailure(d Decision, stage string, err error) error {
	e := &TransportError{Source: d.Source, Proxy: safeProxy(d.Proxy), Stage: stage, cause: err}
	// Resolution errors originate here and are deliberately free of addresses
	// and credentials. Other errors may include upstream-controlled strings.
	log.Print(e.Error())
	return e
}

func failureKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	}
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return "network timeout"
	}
	return "transport failure"
}
