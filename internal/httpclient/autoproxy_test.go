package httpclient

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"
)

func TestWindowsAutoProxyResult(t *testing.T) {
	target, _ := url.Parse("https://model.example/path")
	for _, tt := range []struct {
		name                string
		access              uint32
		proxy, bypass, want string
		fail                bool
	}{
		{name: "direct", access: 1},
		{name: "proxy", access: 3, proxy: "proxy.test:8080", want: "http://proxy.test:8080"},
		{name: "first endpoint", access: 3, proxy: "first.test:80; second.test:80", want: "http://first.test:80"},
		{name: "scheme", access: 3, proxy: "http=other.test:80;https=secure.test:80", want: "http://secure.test:80"},
		{name: "socks", access: 3, proxy: "socks=proxy.test:1080", want: "socks5://proxy.test:1080"},
		{name: "bypass", access: 3, proxy: "proxy.test:80", bypass: "*.example"},
		{name: "empty", access: 3, fail: true},
		{name: "wrong scheme", access: 3, proxy: "http=proxy.test:80", fail: true},
		{name: "unknown access", access: 0, fail: true},
		{name: "invalid", access: 3, proxy: "http://user:secret@%zz", fail: true},
		{name: "raw PAC is not native result", access: 3, proxy: "DIRECT", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := windowsAutoProxyResult(target, tt.access, tt.proxy, tt.bypass)
			if (err != nil) != tt.fail {
				t.Fatalf("proxy=%v err=%v", p, err)
			}
			got := ""
			if p != nil {
				got = p.String()
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestAutoProxyResolutionAndPrecedence(t *testing.T) {
	target, _ := url.Parse("https://model.example/path?q=one")
	for _, tt := range []struct {
		name                        string
		cfg                         Config
		env                         httpproxy.Config
		fixed, bypass, direct, fail bool
		want                        string
		calls                       int
	}{
		{name: "automatic proxy", want: "system-auto", calls: 1},
		{name: "PAC direct", direct: true, want: "system-auto", calls: 1},
		{name: "PAC failure no fallback", fail: true, want: "system-auto", calls: 1},
		{name: "fixed system first", fixed: true, want: "system"},
		{name: "system bypass first", bypass: true, want: "system-bypass"},
		{name: "explicit direct", cfg: Config{Mode: "direct"}, want: "explicit-direct"},
		{name: "explicit proxy", cfg: Config{Mode: "fixed", URL: "fixed.test:80"}, want: "explicit"},
		{name: "environment", env: httpproxy.Config{HTTPSProxy: "env.test:80"}, want: "environment"},
		{name: "environment bypass", env: httpproxy.Config{NoProxy: "*"}, want: "environment-bypass"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			s := systemSettings{Auto: true, ResolveAuto: func(ctx context.Context, u *url.URL) (*url.URL, error) {
				calls++
				if u.String() != target.String() {
					t.Fatal("lost per-URL input")
				}
				if tt.fail {
					return nil, errors.New("PAC failed")
				}
				if tt.direct {
					return nil, nil
				}
				return proxyURL(t, "pac.test:80"), nil
			}}
			if tt.fixed {
				s.HTTPS = proxyURL(t, "fixed.test:80")
			}
			if tt.bypass {
				s.Bypass = []string{"*.example"}
			}
			r, _ := newResolver(tt.cfg, tt.env, func(context.Context) (systemSettings, error) { return s, nil })
			d, err := r.Resolve(context.Background(), target)
			if (err != nil) != tt.fail || d.Source != tt.want || calls != tt.calls {
				t.Fatalf("decision=%+v err=%v calls=%d", d, err, calls)
			}
			if tt.direct && d.Proxy != nil {
				t.Fatal("PAC direct ignored")
			}
		})
	}
}

func TestAutoProxyWorkersCancellationAndBound(t *testing.T) {
	w := autoProxyWorkers{slots: make(chan struct{}, 1), timeout: time.Second}
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := w.resolve(ctx, func() (*url.URL, error) { close(entered); <-release; return nil, nil })
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	_, err := w.resolve(ctx2, func() (*url.URL, error) { t.Error("exceeded native worker bound"); return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestAutoProxyWorkersSuccess(t *testing.T) {
	w := autoProxyWorkers{slots: make(chan struct{}, 1), timeout: time.Second}
	expected := proxyURL(t, "pac.test:80")
	p, err := w.resolve(context.Background(), func() (*url.URL, error) { return expected, nil })
	if err != nil || p != expected {
		t.Fatalf("%v %v", p, err)
	}
}
