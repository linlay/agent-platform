// Package httpclient owns Platform HTTP routing. It never changes net/http's
// globals or exports proxy credentials to child processes.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

const DefaultRefreshInterval = 15 * time.Second

type Config struct {
	Mode                  string // auto (default), direct, or fixed
	URL                   string
	Bypass                []string // NO_PROXY syntax; used only in fixed mode
	SystemRefreshInterval time.Duration
}

func (c Config) Validate() error {
	switch c.Mode {
	case "", "auto", "direct":
		if c.URL != "" || len(c.Bypass) != 0 {
			return errors.New("http-proxy url and bypass require mode: fixed")
		}
	case "fixed":
		if _, err := parseProxy(c.URL); err != nil {
			return fmt.Errorf("http-proxy url: %w", err)
		}
	default:
		return errors.New("http-proxy mode must be auto, direct, or fixed")
	}
	if c.SystemRefreshInterval < 0 {
		return errors.New("http-proxy system-refresh-interval must be positive")
	}
	return nil
}

type Decision struct {
	Proxy  *url.URL // nil is an explicit direct decision, never a fallback signal
	Source string
}

type systemSettings struct {
	HTTP, HTTPS, SOCKS *url.URL
	Bypass             []string
	ExcludeSimple      bool
	Auto               bool
	ResolveAuto        func(context.Context, *url.URL) (*url.URL, error)
}

type systemReader func(context.Context) (systemSettings, error)

type Resolver struct {
	config      Config
	fixed       *url.URL
	fixedBypass func(*url.URL) bool
	env         httpproxy.Config
	envBypass   func(*url.URL) bool
	readSystem  systemReader
	now         func() time.Time
	mu          sync.Mutex
	settings    systemSettings
	settingsErr error
	expires     time.Time
	autoWarning sync.Once
}

func NewResolver(c Config) (*Resolver, error) {
	return newResolver(c, *httpproxy.FromEnvironment(), readSystemSettings)
}

func newResolver(c Config, env httpproxy.Config, read systemReader) (*Resolver, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Mode == "" {
		c.Mode = "auto"
	}
	if c.SystemRefreshInterval == 0 {
		c.SystemRefreshInterval = DefaultRefreshInterval
	}
	r := &Resolver{config: c, env: env, readSystem: read, now: time.Now,
		fixedBypass: noProxyMatcher(strings.Join(c.Bypass, ",")), envBypass: noProxyMatcher(env.NoProxy)}
	if c.Mode == "fixed" {
		r.fixed, _ = parseProxy(c.URL)
	}
	return r, nil
}

// A sentinel proxy lets the standard Go matcher distinguish NO_PROXY from an
// absent scheme-specific proxy, including when only NO_PROXY is configured.
func noProxyMatcher(rules string) func(*url.URL) bool {
	f := (&httpproxy.Config{HTTPProxy: "http://proxy.invalid", HTTPSProxy: "http://proxy.invalid", NoProxy: rules}).ProxyFunc()
	return func(u *url.URL) bool { p, _ := f(u); return p == nil }
}

func (r *Resolver) Resolve(ctx context.Context, u *url.URL) (Decision, error) {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Decision{}, errors.New("HTTP proxy resolver requires an HTTP(S) URL")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return Decision{Source: "loopback"}, nil
	}
	if r.config.Mode == "direct" {
		return Decision{Source: "explicit-direct"}, nil
	}
	if r.config.Mode == "fixed" {
		if r.fixedBypass(u) {
			return Decision{Source: "explicit-bypass"}, nil
		}
		return Decision{Proxy: r.fixed, Source: "explicit"}, nil
	}
	if r.envBypass(u) {
		return Decision{Source: "environment-bypass"}, nil
	}
	raw := r.env.HTTPProxy
	if u.Scheme == "https" {
		raw = r.env.HTTPSProxy
	}
	if raw != "" {
		d := Decision{Source: "environment"}
		if u.Scheme == "http" && r.env.CGI {
			return d, errors.New("HTTP_PROXY is not allowed in a CGI environment")
		}
		p, err := parseProxy(raw)
		d.Proxy = p
		return d, err
	}
	s, err := r.system(ctx)
	if err != nil {
		return Decision{Source: "system"}, fmt.Errorf("read system proxy settings: %w", err)
	}
	if s.Auto && s.ResolveAuto == nil {
		r.autoWarning.Do(func() {
			log.Print("system PAC/WPAD detected; automatic scripts are not executed; only fixed proxies and bypass settings are supported")
		})
	}
	if systemBypass(u, s.Bypass, s.ExcludeSimple) {
		return Decision{Source: "system-bypass"}, nil
	}
	p := s.HTTP
	if u.Scheme == "https" {
		p = s.HTTPS
	}
	if p == nil {
		p = s.SOCKS
	}
	if p != nil {
		return Decision{Proxy: p, Source: "system"}, nil
	}
	if s.Auto && s.ResolveAuto != nil {
		p, err := s.ResolveAuto(ctx, u)
		return Decision{Proxy: p, Source: "system-auto"}, err
	}
	if s.Auto {
		return Decision{Source: "system-auto"}, errors.New("system PAC/WPAD is not supported yet; configure a fixed system proxy, HTTP_PROXY/HTTPS_PROXY, or http-proxy mode")
	}
	return Decision{Source: "direct"}, nil
}

func (r *Resolver) ProxyForRequest(req *http.Request) (*url.URL, error) {
	d, err := r.Resolve(req.Context(), req.URL)
	return d.Proxy, err
}

func (r *Resolver) system(ctx context.Context) (systemSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return systemSettings{}, err
	}
	if r.now().Before(r.expires) {
		return r.settings, r.settingsErr
	}
	// Do not let one canceled request poison the process-wide cache. The OS
	// read has its own short deadline; request cancellation is checked again.
	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.settings, r.settingsErr = r.readSystem(readCtx)
	r.expires = r.now().Add(r.config.SystemRefreshInterval)
	if err := ctx.Err(); err != nil {
		return systemSettings{}, err
	}
	return r.settings, r.settingsErr
}

// Refresh invalidates only the settings cache. Existing streams retain their
// connections; the next request obtains a fresh system snapshot.
func (r *Resolver) Refresh() { r.mu.Lock(); r.expires = time.Time{}; r.mu.Unlock() }

func parseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("proxy address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	// Do not include raw input or url.Parse errors: both can contain passwords.
	if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid proxy address; expected scheme://host:port")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("unsupported proxy scheme; use http, https, socks5, or socks5h")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid proxy port")
		}
	}
	u.Path = ""
	return u, nil
}

func safeProxy(u *url.URL) string {
	if u == nil {
		return "direct"
	}
	return u.Scheme + "://" + u.Host
}
