package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/sys/windows"
)

func TestWindowsWPADFailurePolicy(t *testing.T) {
	for _, tt := range []struct {
		name, pac string
		detect    bool
		err       error
		direct    bool
	}{
		{"missing WPAD", "", true, windows.Errno(12180), true},
		{"wrapped missing WPAD", "", true, fmt.Errorf("native: %w", windows.Errno(12180)), true},
		{"explicit PAC with discovery", "http://pac.test/proxy.pac", true, windows.Errno(12180), false},
		{"explicit PAC", "http://pac.test/proxy.pac", false, windows.Errno(12180), false},
		{"no discovery", "", false, windows.Errno(12180), false},
		{"download failure", "", true, windows.Errno(12167), false},
		{"timeout", "", true, windows.Errno(12002), false},
		{"canceled", "", true, context.Canceled, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := systemSettings{Auto: true, ResolveAuto: func(context.Context, *url.URL) (*url.URL, error) {
				return nil, windowsAutoProxyError(tt.pac, tt.detect, tt.err)
			}}
			r, err := newResolver(Config{}, httpproxy.Config{}, func(context.Context) (systemSettings, error) { return s, nil })
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse("https://model.test/")
			d, err := r.Resolve(context.Background(), u)
			if tt.direct {
				if err != nil || d.Proxy != nil || d.Source != "system-wpad-not-found" {
					t.Fatalf("decision=%+v error=%v", d, err)
				}
			} else if !errors.Is(err, tt.err) || d.Source != "system-auto" {
				t.Fatalf("lost resolution failure: decision=%+v error=%v", d, err)
			}
		})
	}
}

// Exercises the real WinHTTP PAC evaluator without modifying system settings
// or contacting the model host. Run this test on a native Windows runner.
func TestWindowsNativePAC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		_, _ = w.Write([]byte(`function FindProxyForURL(url, host) {
   if (host == "direct.example") return "DIRECT";
   return "PROXY 127.0.0.1:18080";
  }`))
	}))
	defer server.Close()
	for _, tt := range []struct{ host, want string }{{"direct.example", ""}, {"model.example", "http://127.0.0.1:18080"}} {
		t.Run(tt.host, func(t *testing.T) {
			target, _ := url.Parse("https://" + tt.host + "/v1/chat/completions")
			p, err := resolveWindowsAutoProxy(server.URL+"/proxy.pac", false, target)
			if err != nil {
				t.Fatal(err)
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
