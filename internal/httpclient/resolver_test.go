package httpclient

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http/httpproxy"
)

func proxyURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := parseProxy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestResolutionPrecedence(t *testing.T) {
	system := systemSettings{HTTP: proxyURL(t, "http://system.test:8080"), HTTPS: proxyURL(t, "http://secure.test:8080"), SOCKS: proxyURL(t, "socks5://socks.test:1080"), Bypass: []string{"*.internal", "10.0.0.0/8", "<local>"}}
	for _, tt := range []struct {
		name, target string
		config       Config
		env          httpproxy.Config
		want, source string
		reads        int
	}{
		{name: "system http", target: "http://example.org", want: "http://system.test:8080", source: "system", reads: 1},
		{name: "system https", target: "https://example.org", want: "http://secure.test:8080", source: "system", reads: 1},
		{name: "explicit direct", target: "https://example.org", config: Config{Mode: "direct"}, env: httpproxy.Config{HTTPSProxy: "env.test:80"}, source: "explicit-direct"},
		{name: "explicit fixed", target: "https://example.org", config: Config{Mode: "fixed", URL: "fixed.test:80"}, env: httpproxy.Config{NoProxy: "*"}, want: "http://fixed.test:80", source: "explicit"},
		{name: "explicit bypass", target: "https://example.org", config: Config{Mode: "fixed", URL: "fixed.test:80", Bypass: []string{"example.org"}}, source: "explicit-bypass"},
		{name: "environment", target: "https://example.org", env: httpproxy.Config{HTTPSProxy: "env.test:80"}, want: "http://env.test:80", source: "environment"},
		{name: "env absent for scheme", target: "https://example.org", env: httpproxy.Config{HTTPProxy: "env.test:80"}, want: "http://secure.test:8080", source: "system", reads: 1},
		{name: "environment no proxy", target: "https://api.example.org", env: httpproxy.Config{HTTPSProxy: "env.test:80", NoProxy: "example.org"}, source: "environment-bypass"},
		{name: "no proxy alone", target: "https://api.example.org", env: httpproxy.Config{NoProxy: "example.org"}, source: "environment-bypass"},
		{name: "no proxy all", target: "https://example.org", env: httpproxy.Config{NoProxy: "*"}, source: "environment-bypass"},
		{name: "no proxy CIDR", target: "https://10.1.2.3", env: httpproxy.Config{NoProxy: "10.0.0.0/8"}, source: "environment-bypass"},
		{name: "no proxy IPv6", target: "https://[2001:db8::1]", env: httpproxy.Config{NoProxy: "2001:db8::/32"}, source: "environment-bypass"},
		{name: "no proxy port", target: "https://example.org:8443", env: httpproxy.Config{NoProxy: "example.org:8443"}, source: "environment-bypass"},
		{name: "no proxy port mismatch", target: "https://example.org", env: httpproxy.Config{NoProxy: "example.org:8443"}, want: "http://secure.test:8080", source: "system", reads: 1},
		{name: "system bypass", target: "https://api.internal", source: "system-bypass", reads: 1},
		{name: "system simple host", target: "http://intranet", source: "system-bypass", reads: 1},
		{name: "localhost", target: "http://localhost:8080", config: Config{Mode: "fixed", URL: "fixed.test:80"}, source: "loopback"},
		{name: "localhost dot", target: "http://localhost.:8080", source: "loopback"},
		{name: "loopback IPv4", target: "http://127.0.1.2:8080", source: "loopback"},
		{name: "loopback IPv6", target: "http://[::1]:8080", source: "loopback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			r, err := newResolver(tt.config, tt.env, func(context.Context) (systemSettings, error) { reads++; return system, nil })
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(tt.target)
			d, err := r.Resolve(context.Background(), u)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if d.Proxy != nil {
				got = d.Proxy.String()
			}
			if got != tt.want || d.Source != tt.source || reads != tt.reads {
				t.Fatalf("got %q %q reads=%d; want %q %q reads=%d", got, d.Source, reads, tt.want, tt.source, tt.reads)
			}
		})
	}
}

func TestSystemModesAndErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		settings systemSettings
		env      httpproxy.Config
		readErr  error
		want     string
		fail     bool
	}{
		{name: "unconfigured", want: "direct"},
		{name: "socks", settings: systemSettings{SOCKS: proxyURL(t, "socks5://proxy.test:1080")}, want: "system"},
		{name: "PAC only", settings: systemSettings{Auto: true}, fail: true},
		{name: "PAC with fixed", settings: systemSettings{Auto: true, HTTPS: proxyURL(t, "proxy.test:80")}, want: "system"},
		{name: "read failed", readErr: errors.New("OS read failed"), fail: true},
		{name: "invalid environment", env: httpproxy.Config{HTTPSProxy: "http://user:secret@%zz"}, fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := newResolver(Config{}, tt.env, func(context.Context) (systemSettings, error) { return tt.settings, tt.readErr })
			u, _ := url.Parse("https://example.org")
			d, err := r.Resolve(context.Background(), u)
			if (err != nil) != tt.fail {
				t.Fatalf("got decision=%+v err=%v", d, err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("credential leak")
				}
				return
			}
			if d.Source != tt.want {
				t.Fatalf("got source %q", d.Source)
			}
		})
	}
}

func TestSystemCacheRefreshAndFailure(t *testing.T) {
	var reads atomic.Int32
	r, _ := newResolver(Config{}, httpproxy.Config{}, func(context.Context) (systemSettings, error) {
		n := reads.Add(1)
		if n == 3 {
			return systemSettings{}, errors.New("temporary read failure")
		}
		if n >= 4 {
			return systemSettings{}, nil
		}
		return systemSettings{HTTPS: proxyURL(t, "proxy.test:"+map[int32]string{1: "8001", 2: "8002"}[n])}, nil
	})
	now := time.Now()
	r.now = func() time.Time { return now }
	u, _ := url.Parse("https://example.org")
	var wg sync.WaitGroup
	for range 25 {
		wg.Go(func() {
			if _, err := r.Resolve(context.Background(), u); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("read system %d times", reads.Load())
	}
	r.Refresh()
	d, err := r.Resolve(context.Background(), u)
	if err != nil || d.Proxy.Port() != "8002" {
		t.Fatalf("refresh: %+v %v", d, err)
	}
	now = now.Add(DefaultRefreshInterval)
	if _, err = r.Resolve(context.Background(), u); err == nil {
		t.Fatal("must not use stale proxy or direct on refresh error")
	}
	if _, err = r.Resolve(context.Background(), u); err == nil || reads.Load() != 3 {
		t.Fatal("error must be cached")
	}
	now = now.Add(DefaultRefreshInterval)
	d, err = r.Resolve(context.Background(), u)
	if err != nil || d.Proxy != nil || d.Source != "direct" {
		t.Fatalf("proxy disable: %+v %v", d, err)
	}
}

func TestEnvironmentCasePrecedence(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}
	t.Setenv("https_proxy", "lower.test:80")
	t.Setenv("HTTPS_PROXY", "upper.test:80")
	r, err := NewResolver(Config{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://example.org")
	d, err := r.Resolve(context.Background(), u)
	if err != nil || d.Proxy.Host != "upper.test:80" {
		t.Fatalf("%+v %v", d, err)
	}
	t.Setenv("HTTPS_PROXY", "")
	r, _ = NewResolver(Config{})
	d, err = r.Resolve(context.Background(), u)
	if err != nil || d.Proxy.Host != "lower.test:80" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestSystemBypass(t *testing.T) {
	for _, tt := range []struct {
		rule, target string
		match        bool
	}{
		{"*.example.org", "https://api.example.org", true},
		{"*.example.org", "https://example.org", false},
		{"example.org", "https://api.example.org", false},
		{"<local>", "http://printer", true},
		{"<local>", "http://10.1.2.3", false},
		{"10.0.0.0/8", "http://10.1.2.3", true},
		{"169.254/16", "http://169.254.1.2", true},
		{"2001:db8::/32", "http://[2001:db8::1]", true},
		{"[2001:db8::1]:443", "https://[2001:db8::1]", true},
		{"https://example.org:443", "https://example.org", true},
		{"https://example.org:443", "http://example.org", false},
		{"192.168.*", "http://192.168.1.2", true},
		{"*", "https://example.org", true},
	} {
		t.Run(tt.rule+tt.target, func(t *testing.T) {
			u, _ := url.Parse(tt.target)
			if got := systemBypass(u, []string{tt.rule}, false); got != tt.match {
				t.Fatalf("got %v", got)
			}
		})
	}
}
