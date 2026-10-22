package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/httpclient"
	"agent-platform/internal/models"
)

// speechRequest is the protocol-neutral synthesis request. Optional controls
// stay nil or empty when the caller omitted them; each protocol rejects the
// controls it cannot express instead of dropping them.
type speechRequest struct {
	Text       string
	Voice      string
	Language   string
	Speed      *float64
	Pitch      *int
	Volume     *float64
	Emotion    string
	Format     string
	SampleRate int
}

// speechResponse is what a protocol extracted from a successful provider
// response. Exactly one of Audio and AudioURL is set.
type speechResponse struct {
	Audio      []byte
	AudioURL   string
	DurationMs int
	// Characters is the provider-reported billed character count; zero when
	// the provider did not report one.
	Characters int
	RequestID  string
}

// speechProtocol owns one provider wire format: the request body it sends
// and the response it decodes.
type speechProtocol interface {
	buildBody(model models.ModelDefinition, req speechRequest) (map[string]any, error)
	parseResponse(statusCode int, body []byte) (speechResponse, error)
}

var speechProtocols = map[string]speechProtocol{
	models.ProtocolMiniMaxTTS:   minimaxTTSProtocol{},
	models.ProtocolDashScopeTTS: dashscopeTTSProtocol{},
}

// errSpeechParamUnsupported marks a request control the protocol cannot send.
type errSpeechParamUnsupported struct {
	Param    string
	Protocol string
	Detail   string
}

func (e errSpeechParamUnsupported) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s is not supported by protocol %s: %s", e.Param, e.Protocol, e.Detail)
	}
	return fmt.Sprintf("%s is not supported by protocol %s", e.Param, e.Protocol)
}

// errSpeechProviderStatus is a provider-reported failure. Message carries the
// provider's own error code and message, never the raw body or credentials.
type errSpeechProviderStatus struct {
	StatusCode int
	Code       string
	Message    string
}

func (e errSpeechProviderStatus) Error() string {
	parts := []string{}
	if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("status %d", e.StatusCode))
	}
	if e.Code != "" {
		parts = append(parts, "code "+e.Code)
	}
	text := "speech provider request failed"
	if len(parts) > 0 {
		text += " with " + strings.Join(parts, ", ")
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

const speechProviderMessageLimit = 300

func boundedProviderMessage(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= speechProviderMessageLimit {
		return value
	}
	runes := []rune(value)
	return string(runes[:speechProviderMessageLimit]) + "…"
}

// speechResponseLimit bounds the provider JSON body: hex audio doubles the
// byte count, so allow twice the audio limit plus envelope room.
func speechResponseLimit(maxAudioBytes int) int64 {
	return int64(maxAudioBytes)*2 + 1<<20
}

func (t *RuntimeToolExecutor) postSpeechRequest(ctx context.Context, provider models.ProviderDefinition, model models.ModelDefinition, body map[string]any, maxAudioBytes int) (int, []byte, string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, "", err
	}
	endpoint := strings.TrimRight(provider.BaseURL, "/") + provider.ResolvedEndpointPath(model.Protocol)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	authHeader, authValue, err := provider.AuthHeader("")
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set(authHeader, authValue)
	for key, value := range provider.Protocol(model.Protocol).Headers {
		req.Header.Set(key, value)
	}
	for key, value := range model.Headers {
		req.Header.Set(key, value)
	}
	client := t.httpClient
	if client == nil {
		client = httpclient.NewClient(0)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	limit := speechResponseLimit(maxAudioBytes)
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, "", err
	}
	if int64(len(data)) > limit {
		return resp.StatusCode, nil, "", fmt.Errorf("speech provider response exceeds %d bytes", limit)
	}
	requestID := ""
	if header := strings.TrimSpace(provider.RequestIDHeader); header != "" {
		requestID = strings.TrimSpace(resp.Header.Get(header))
	}
	return resp.StatusCode, data, requestID, nil
}

func (t *RuntimeToolExecutor) downloadSpeechAudio(ctx context.Context, rawURL string, maxBytes int) ([]byte, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("provider audio URL must use http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	client := t.httpClient
	if client == nil {
		client = httpclient.NewClient(0)
	}
	resp, err := client.Do(req)
	if err != nil {
		// The URL carries a provider signature; report only the host.
		return nil, fmt.Errorf("download audio from %s failed", parsed.Host)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider audio download failed with status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxBytes {
		return nil, fmt.Errorf("provider audio exceeds %d bytes or is empty", maxBytes)
	}
	return data, nil
}

// detectSpeechAudio identifies audio from its leading bytes so a provider
// error page or truncated payload is never saved as audio.
func detectSpeechAudio(data []byte) (format string, mimeType string, ok bool) {
	switch {
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "wav", "audio/wav", true
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("fLaC")):
		return "flac", "audio/flac", true
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("OggS")):
		return "ogg", "audio/ogg", true
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")):
		return "mp3", "audio/mpeg", true
	case len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "mp3", "audio/mpeg", true
	default:
		return "", "", false
	}
}
