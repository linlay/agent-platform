package tools

import (
	"net/http"
	"testing"
	"time"
)

func TestDeepSeekWebRetryDelayLeavesRateLimitWindow(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		status int
		try    int
		want   time.Duration
	}{
		{"rate limit first retry", "", http.StatusTooManyRequests, 0, time.Second},
		{"rate limit second retry", "", http.StatusTooManyRequests, 1, 2 * time.Second},
		{"invalid rate limit hint", "invalid", http.StatusTooManyRequests, 0, time.Second},
		{"explicit immediate retry", "0", http.StatusTooManyRequests, 0, 0},
		{"explicit longer retry", "3", http.StatusTooManyRequests, 0, 3 * time.Second},
		{"transient server retry", "", http.StatusServiceUnavailable, 0, 250 * time.Millisecond},
		{"transient second retry", "", http.StatusServiceUnavailable, 1, 500 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := deepSeekWebRetryDelay(test.header, test.try, test.status); got != test.want {
				t.Fatalf("retry delay = %s, want %s", got, test.want)
			}
		})
	}
}
