package ws

import (
	"testing"
	"time"
)

func TestAuthenticatedDesktopMainRequiresAppSubjectAndVerifiedDevice(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		change  func(*AuthSession)
		allowed bool
	}{
		{name: "authenticated main", source: desktopMainClientSource, allowed: true},
		{name: "ordinary webclient", source: "webclient"},
		{name: "btw", source: desktopBTWClientSource},
		{name: "explain", source: desktopExplainClientSource},
		{name: "missing subject", source: desktopMainClientSource, change: func(a *AuthSession) { a.Subject = "" }},
		{name: "blank subject", source: desktopMainClientSource, change: func(a *AuthSession) { a.Subject = " " }},
		{name: "missing device", source: desktopMainClientSource, change: func(a *AuthSession) { a.DeviceID = "" }},
		{name: "wrong device", source: desktopMainClientSource, change: func(a *AuthSession) { a.DeviceID = "other-device" }},
		{name: "unverified device", source: desktopMainClientSource, change: func(a *AuthSession) { a.DeviceIDVerified = false }},
		{name: "non-app scope", source: desktopMainClientSource, change: func(a *AuthSession) { a.Scope = "openid" }},
		{name: "auth disabled", source: desktopMainClientSource, change: func(a *AuthSession) { a.AuthDisabled = true }},
		{name: "unexpired token", source: desktopMainClientSource, change: func(a *AuthSession) { a.ExpiresAt = time.Now().Add(time.Hour).UnixMilli() }, allowed: true},
		{name: "expired token", source: desktopMainClientSource, change: func(a *AuthSession) { a.ExpiresAt = time.Now().Add(-time.Second).UnixMilli() }},
		{name: "expiry reached", source: desktopMainClientSource, change: func(a *AuthSession) { a.ExpiresAt = time.Now().UnixMilli() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := selectionExplainTestAuth()
			if test.change != nil {
				test.change(&auth)
			}
			conn := desktopLaneTestConn(nil, test.source, auth)
			defer conn.close(1000, "test complete")
			if got := conn.IsAuthenticatedDesktopMain(); got != test.allowed {
				t.Fatalf("authenticated Desktop Main = %v, want %v", got, test.allowed)
			}
		})
	}
	var conn *Conn
	if conn.IsAuthenticatedDesktopMain() {
		t.Fatal("nil connection authorized")
	}
}
