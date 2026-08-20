package tunnelurl

import "testing"

func TestIsLoopbackHostname(t *testing.T) {
	for _, hostname := range []string{"localhost", "LOCALHOST", "127.0.0.1", "::1"} {
		if !IsLoopbackHostname(hostname) {
			t.Fatalf("expected canonical loopback hostname %q to be accepted", hostname)
		}
	}
	for _, hostname := range []string{"127.0.0.2", "demo.localhost", "0.0.0.0", "192.0.2.1"} {
		if IsLoopbackHostname(hostname) {
			t.Fatalf("expected non-canonical loopback hostname %q to be rejected", hostname)
		}
	}
}

func TestIsForbiddenHostname(t *testing.T) {
	for _, hostname := range []string{"127.0.0.2", "127.255.255.255", "demo.localhost", "0.0.0.0"} {
		if !IsForbiddenHostname(hostname) {
			t.Fatalf("expected reserved local hostname %q to be rejected", hostname)
		}
	}
	for _, hostname := range []string{"localhost", "127.0.0.1", "::1", "relay.example.test", "192.0.2.1"} {
		if IsForbiddenHostname(hostname) {
			t.Fatalf("expected allowed hostname %q not to be rejected", hostname)
		}
	}
}
