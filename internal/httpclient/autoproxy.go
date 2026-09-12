package httpclient

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// WinHTTP's synchronous PAC evaluator cannot be interrupted safely by closing
// its session from another goroutine. Bound both caller latency and outstanding
// native calls; the worker owns and eventually frees all native resources.
type autoProxyWorkers struct {
	slots   chan struct{}
	timeout time.Duration
}

func (w autoProxyWorkers) resolve(ctx context.Context, call func() (*url.URL, error)) (*url.URL, error) {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case w.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type result struct {
		proxy *url.URL
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-w.slots }()
		if err := ctx.Err(); err != nil {
			done <- result{err: err}
			return
		}
		p, err := call()
		done <- result{p, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return r.proxy, r.err
	}
}

// Parse WINHTTP_PROXY_INFO, not raw PAC JavaScript output. Only an explicit
// NO_PROXY result or native bypass permits direct access. Select the first
// applicable endpoint; transport failure does not replay a model POST elsewhere.
func windowsAutoProxyResult(target *url.URL, access uint32, proxy, bypass string) (*url.URL, error) {
	if access == 1 {
		return nil, nil
	} // WINHTTP_ACCESS_TYPE_NO_PROXY
	if access != 3 {
		return nil, errors.New("unexpected WinHTTP automatic proxy access type")
	}
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(c rune) bool { return c == ';' || c == ' ' || c == '\t' || c == '\r' || c == '\n' })
	}
	if systemBypass(target, split(bypass), false) {
		return nil, nil
	}
	for _, entry := range split(proxy) {
		kind, address, scoped := strings.Cut(entry, "=")
		if !scoped {
			address = entry
		} else if !strings.EqualFold(kind, target.Scheme) && !strings.EqualFold(kind, "socks") {
			continue
		}
		if scoped && strings.EqualFold(kind, "socks") && !strings.Contains(address, "://") {
			address = "socks5://" + address
		}
		if strings.EqualFold(address, "DIRECT") {
			return nil, errors.New("invalid WinHTTP named proxy result")
		}
		return parseProxy(address)
	}
	return nil, errors.New("WinHTTP automatic proxy returned no applicable endpoint")
}
