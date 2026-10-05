package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFetchGatewayDownloadSizeLimit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		size    int64
		chunked bool
	}{
		{name: "small", size: 17},
		{name: "exact limit", size: gatewayDownloadMaxBytes},
		{name: "over limit with length", size: gatewayDownloadMaxBytes + 1},
		{name: "over limit without length", size: gatewayDownloadMaxBytes + 1, chunked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.chunked {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", strconv.FormatInt(tt.size, 10))
				}
				_, _ = io.CopyN(w, gatewayDownloadZeroReader{}, tt.size)
			}))
			defer gateway.Close()

			server := &Server{}
			data, err := server.fetchGatewayDownload(context.Background(), "chat-download", gateway.URL)
			if tt.size > gatewayDownloadMaxBytes {
				if err == nil || !strings.Contains(err.Error(), "100 MiB") {
					t.Fatalf("expected explicit size error, got %v", err)
				}
				if data != nil {
					t.Fatalf("oversized download returned %d bytes for upload", len(data))
				}
				return
			}
			if err != nil || int64(len(data)) != tt.size {
				t.Fatalf("download bytes=%d, want=%d, err=%v", len(data), tt.size, err)
			}
		})
	}
}

type gatewayDownloadZeroReader struct{}

func (gatewayDownloadZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
