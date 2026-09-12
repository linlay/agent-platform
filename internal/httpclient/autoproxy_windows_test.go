package httpclient

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

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
