package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/models"
)

// dashscopeTTSProtocol implements Qwen TTS on DashScope
// (POST /api/v1/services/aigc/multimodal-generation/generation). The
// non-streaming response carries a temporary output.audio.url (24h) or
// base64 output.audio.data; output is 24kHz WAV and voice controls beyond
// voice and language are not part of the API.
type dashscopeTTSProtocol struct{}

func (dashscopeTTSProtocol) buildBody(model models.ModelDefinition, req speechRequest) (map[string]any, error) {
	protocol := models.ProtocolDashScopeTTS
	switch {
	case req.Speed != nil:
		return nil, errSpeechParamUnsupported{Param: "speed", Protocol: protocol}
	case req.Pitch != nil:
		return nil, errSpeechParamUnsupported{Param: "pitch", Protocol: protocol}
	case req.Volume != nil:
		return nil, errSpeechParamUnsupported{Param: "volume", Protocol: protocol}
	case req.Emotion != "":
		return nil, errSpeechParamUnsupported{Param: "emotion", Protocol: protocol}
	case req.SampleRate != 0:
		return nil, errSpeechParamUnsupported{Param: "sampleRate", Protocol: protocol}
	case req.Format != "" && req.Format != "wav":
		return nil, errSpeechParamUnsupported{Param: "format", Protocol: protocol, Detail: "output is wav only"}
	}
	input := map[string]any{
		"text":  req.Text,
		"voice": req.Voice,
	}
	if req.Language != "" {
		input["language_type"] = req.Language
	}
	return map[string]any{"model": model.ModelID, "input": input}, nil
}

type dashscopeTTSResponse struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Output    *struct {
		Audio *struct {
			URL  string `json:"url"`
			Data string `json:"data"`
		} `json:"audio"`
	} `json:"output"`
	Usage *struct {
		Characters int `json:"characters"`
	} `json:"usage"`
}

func (dashscopeTTSProtocol) parseResponse(statusCode int, body []byte) (speechResponse, error) {
	var decoded dashscopeTTSResponse
	decodeErr := json.Unmarshal(body, &decoded)
	failed := speechResponse{RequestID: strings.TrimSpace(decoded.RequestID)}
	if statusCode < 200 || statusCode >= 300 {
		return failed, errSpeechProviderStatus{StatusCode: statusCode, Code: strings.TrimSpace(decoded.Code), Message: boundedProviderMessage(decoded.Message)}
	}
	if decodeErr != nil {
		return failed, fmt.Errorf("decode DashScope TTS response: %w", decodeErr)
	}
	if code := strings.TrimSpace(decoded.Code); code != "" {
		return failed, errSpeechProviderStatus{Code: code, Message: boundedProviderMessage(decoded.Message)}
	}
	// A successful response is billed; keep its characters even if the
	// audio part below turns out unusable.
	out := speechResponse{RequestID: failed.RequestID}
	if decoded.Usage != nil {
		out.Characters = decoded.Usage.Characters
	}
	if decoded.Output == nil || decoded.Output.Audio == nil {
		return out, fmt.Errorf("DashScope TTS response has no output.audio")
	}
	if url := strings.TrimSpace(decoded.Output.Audio.URL); url != "" {
		out.AudioURL = url
		return out, nil
	}
	data := strings.TrimSpace(decoded.Output.Audio.Data)
	if data == "" {
		return out, fmt.Errorf("DashScope TTS response has neither audio url nor data")
	}
	audio, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return out, fmt.Errorf("decode DashScope base64 audio: %w", err)
	}
	out.Audio = audio
	return out, nil
}
