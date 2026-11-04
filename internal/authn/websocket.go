package authn

import (
	"net/http"
	"strings"

	gws "github.com/gorilla/websocket"
)

// BearerSubprotocol is shared with the media WS HTTP authentication boundary.
// It deliberately does not extract URL query credentials.
func BearerSubprotocol(r *http.Request) (string, string) {
	for _, protocol := range gws.Subprotocols(r) {
		trimmed := strings.TrimSpace(protocol)
		if strings.HasPrefix(trimmed, "bearer.") && len(trimmed) > len("bearer.") {
			return strings.TrimPrefix(trimmed, "bearer."), trimmed
		}
	}
	return "", ""
}
