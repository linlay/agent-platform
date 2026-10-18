// Package modelclient owns provider HTTP execution, first-response timeout,
// response classification, and stream body lifecycle.
package modelclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/observability"
)

type Client struct {
	http *http.Client
}

type Stream struct {
	Body     io.ReadCloser
	Cancel   context.CancelFunc
	Response ResponseMetadata
}

// ResponseMetadata contains only bounded, allowlisted transport diagnostics.
// Never retain arbitrary headers (cookies and credentials may be present).
type ResponseMetadata struct {
	StatusCode      int    `json:"httpStatus"`
	ContentType     string `json:"contentType,omitempty"`
	RequestID       string `json:"upstreamRequestId,omitempty"`
	RequestIDHeader string `json:"upstreamRequestIdHeader,omitempty"`
}

var defaultRequestIDHeaders = []string{"X-Request-Id", "Request-Id", "X-Ms-Request-Id", "X-Amzn-Requestid"}

func boundedHeaderValue(value string) string {
	value = observability.SanitizeLog(strings.TrimSpace(value))
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

// responseMetadata checks provider-configured request ID headers (a relay's
// own trace header) before the generic ones; it still reads only named headers.
func responseMetadata(response *http.Response, requestIDHeaders []string) ResponseMetadata {
	metadata := ResponseMetadata{StatusCode: response.StatusCode, ContentType: boundedHeaderValue(response.Header.Get("Content-Type"))}
	metadata.RequestID, metadata.RequestIDHeader = upstreamRequestID(response.Header, requestIDHeaders)
	return metadata
}

func upstreamRequestID(header http.Header, requestIDHeaders []string) (string, string) {
	for _, keys := range [][]string{requestIDHeaders, defaultRequestIDHeaders} {
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if value := boundedHeaderValue(header.Get(key)); value != "" {
				return value, http.CanonicalHeaderKey(key)
			}
		}
	}
	return "", ""
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpclient.NewClient(0)
	}
	return &Client{http: httpClient}
}

// OpenStream reports the upstream request ID from requestIDHeaders (then the
// generic headers) on both the opened stream and HTTP error diagnostics.
func (c *Client) OpenStream(request *http.Request, firstResponseTimeout time.Duration, requestIDHeaders ...string) (*Stream, error) {
	if request == nil {
		return nil, apperrors.New(apperrors.CodeProviderBadRequest, "provider request is required")
	}
	if firstResponseTimeout <= 0 {
		return c.do(request, requestIDHeaders)
	}
	ctx, cancel := context.WithCancel(request.Context())
	timedRequest := request.WithContext(ctx)
	type result struct {
		stream *Stream
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		stream, err := c.do(timedRequest, requestIDHeaders)
		resultCh <- result{stream: stream, err: err}
	}()
	timer := time.NewTimer(firstResponseTimeout)
	defer timer.Stop()
	select {
	case response := <-resultCh:
		if response.err != nil {
			cancel()
			return nil, response.err
		}
		if response.stream == nil || response.stream.Body == nil {
			cancel()
			return nil, errors.New("provider returned no stream")
		}
		response.stream.Cancel = cancel
		return response.stream, nil
	case <-timer.C:
		cancel()
		return nil, StreamTimeoutError(firstResponseTimeout)
	}
}

func (c *Client) do(request *http.Request, requestIDHeaders []string) (*Stream, error) {
	client := c.http
	if client == nil {
		client = httpclient.NewClient(0)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, TransportError(err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			body = nil
		}
		requestID, headerName := upstreamRequestID(response.Header, requestIDHeaders)
		return nil, responseError(response.StatusCode, body, requestID, headerName)
	}
	return &Stream{Body: response.Body, Response: responseMetadata(response, requestIDHeaders)}, nil
}

func StreamTimeoutError(timeout time.Duration) error {
	seconds := int64(timeout / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return apperrors.New(
		apperrors.CodeProviderTimeout,
		fmt.Sprintf("model stream idle timeout after %d seconds", seconds),
		apperrors.WithDiagnostic("timeoutSeconds", seconds),
		apperrors.WithDiagnostic("reason", "model_stream_idle_timeout"),
	)
}

func TransportError(err error) error {
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		return err
	}
	code := apperrors.CodeProviderNetworkError
	status := http.StatusBadGateway
	lower := strings.ToLower(err.Error())
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") {
		code = apperrors.CodeProviderTimeout
		status = http.StatusGatewayTimeout
	}
	return apperrors.Wrap(code, err, apperrors.WithStatus(status))
}

func ResponseError(status int, body []byte) error {
	return responseError(status, body, "", "")
}

func responseError(status int, body []byte, requestID string, requestIDHeader string) error {
	bodyText := strings.TrimSpace(string(body))
	fields := ParseErrorFields(body)
	code := ClassifyError(status, fields.Code, fields.Type, apperrors.CodeProviderRequestFailed)
	message := fmt.Sprintf("model request failed with status %d", status)
	if bodyText != "" {
		message += ": " + bodyText
	}
	diagnostics := map[string]any{"upstreamStatus": status}
	if bodyText != "" {
		diagnostics["upstreamBody"] = bodyText
	}
	if fields.Code != "" {
		diagnostics["upstreamCode"] = fields.Code
	}
	if fields.Type != "" {
		diagnostics["upstreamType"] = fields.Type
	}
	if fields.Message != "" {
		diagnostics["upstreamMessage"] = fields.Message
	}
	if requestID != "" {
		diagnostics["upstreamRequestId"], diagnostics["upstreamRequestIdHeader"] = requestID, requestIDHeader
	}
	return apperrors.New(code, message, apperrors.WithDiagnostics(diagnostics))
}

func ClassifyResponseError(status int, body string) (apperrors.Code, string) {
	fields := ParseErrorFields([]byte(body))
	return ClassifyError(status, fields.Code, fields.Type, apperrors.CodeProviderRequestFailed), fields.Code
}
