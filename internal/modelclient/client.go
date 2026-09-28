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

func responseMetadata(response *http.Response) ResponseMetadata {
	bounded := func(value string) string {
		value = observability.SanitizeLog(strings.TrimSpace(value))
		if len(value) > 256 {
			value = value[:256]
		}
		return value
	}
	metadata := ResponseMetadata{StatusCode: response.StatusCode, ContentType: bounded(response.Header.Get("Content-Type"))}
	for _, key := range []string{"X-Request-Id", "Request-Id", "X-Ms-Request-Id", "X-Amzn-Requestid"} {
		if value := bounded(response.Header.Get(key)); value != "" {
			metadata.RequestID, metadata.RequestIDHeader = value, key
			break
		}
	}
	return metadata
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpclient.NewClient(0)
	}
	return &Client{http: httpClient}
}

func (c *Client) OpenStream(request *http.Request, firstResponseTimeout time.Duration) (*Stream, error) {
	if request == nil {
		return nil, apperrors.New(apperrors.CodeProviderBadRequest, "provider request is required")
	}
	if firstResponseTimeout <= 0 {
		return c.do(request)
	}
	ctx, cancel := context.WithCancel(request.Context())
	timedRequest := request.WithContext(ctx)
	type result struct {
		stream *Stream
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		stream, err := c.do(timedRequest)
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

func (c *Client) do(request *http.Request) (*Stream, error) {
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
			return nil, ResponseError(response.StatusCode, nil)
		}
		return nil, ResponseError(response.StatusCode, body)
	}
	return &Stream{Body: response.Body, Response: responseMetadata(response)}, nil
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
	return apperrors.New(code, message, apperrors.WithDiagnostics(diagnostics))
}

func ClassifyResponseError(status int, body string) (apperrors.Code, string) {
	fields := ParseErrorFields([]byte(body))
	return ClassifyError(status, fields.Code, fields.Type, apperrors.CodeProviderRequestFailed), fields.Code
}
