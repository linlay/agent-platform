package observability

import (
	"net/http"
	"time"
)

func LogRequest(r *http.Request, status int, cost time.Duration) {
	if r == nil {
		return
	}
	Log("http.request", map[string]any{
		"method":   r.Method,
		"path":     r.URL.Path,
		"query":    SanitizeLog(r.URL.RawQuery),
		"status":   status,
		"costMs":   cost.Milliseconds(),
		"remoteIP": r.RemoteAddr,
	})
}
