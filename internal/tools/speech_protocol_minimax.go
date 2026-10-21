package tools

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/models"
)

// minimaxTTSProtocol implements MiniMax synchronous T2A (POST /v1/t2a_v2):
// a flat JSON request and hex-encoded audio in data.audio. A failure may
// arrive as HTTP 200 with a non-zero base_resp.status_code.
type minimaxTTSProtocol struct{}

var (
	minimaxTTSFormats     = map[string]bool{"mp3": true, "wav": true, "flac": true}
	minimaxTTSSampleRates = map[int]bool{8000: true, 16000: true, 22050: true, 24000: true, 32000: true, 44100: true}
)

func (minimaxTTSProtocol) buildBody(model models.ModelDefinition, req speechRequest) (map[string]any, error) {
	protocol := models.ProtocolMiniMaxTTS
	voice := map[string]any{"voice_id": req.Voice}
	if req.Speed != nil {
		if *req.Speed < 0.5 || *req.Speed > 2 {
			return nil, errSpeechParamUnsupported{Param: "speed", Protocol: protocol, Detail: "must be between 0.5 and 2"}
		}
		voice["speed"] = *req.Speed
	}
	if req.Volume != nil {
		if *req.Volume <= 0 || *req.Volume > 10 {
			return nil, errSpeechParamUnsupported{Param: "volume", Protocol: protocol, Detail: "must be greater than 0 and at most 10"}
		}
		voice["vol"] = *req.Volume
	}
	if req.Pitch != nil {
		if *req.Pitch < -12 || *req.Pitch > 12 {
			return nil, errSpeechParamUnsupported{Param: "pitch", Protocol: protocol, Detail: "must be between -12 and 12"}
		}
		voice["pitch"] = *req.Pitch
	}
	if req.Emotion != "" {
		voice["emotion"] = req.Emotion
	}
	audio := map[string]any{}
	if req.Format != "" {
		if !minimaxTTSFormats[req.Format] {
			return nil, errSpeechParamUnsupported{Param: "format", Protocol: protocol, Detail: "must be mp3, wav or flac"}
		}
		audio["format"] = req.Format
	}
	if req.SampleRate != 0 {
		if !minimaxTTSSampleRates[req.SampleRate] {
			return nil, errSpeechParamUnsupported{Param: "sampleRate", Protocol: protocol, Detail: "must be 8000, 16000, 22050, 24000, 32000 or 44100"}
		}
		audio["sample_rate"] = req.SampleRate
	}
	body := map[string]any{
		"model":         model.ModelID,
		"text":          req.Text,
		"stream":        false,
		"output_format": "hex",
		"voice_setting": voice,
	}
	if len(audio) > 0 {
		body["audio_setting"] = audio
	}
	if req.Language != "" {
		body["language_boost"] = req.Language
	}
	return body, nil
}

type minimaxTTSResponse struct {
	Data *struct {
		Audio string `json:"audio"`
	} `json:"data"`
	ExtraInfo *struct {
		UsageCharacters int `json:"usage_characters"`
		AudioLength     int `json:"audio_length"`
	} `json:"extra_info"`
	TraceID  string `json:"trace_id"`
	BaseResp *struct {
		StatusCode json.Number `json:"status_code"`
		StatusMsg  string      `json:"status_msg"`
	} `json:"base_resp"`
}

func (minimaxTTSProtocol) parseResponse(statusCode int, body []byte) (speechResponse, error) {
	var decoded minimaxTTSResponse
	decodeErr := json.Unmarshal(body, &decoded)
	// Failures keep the trace ID for troubleshooting but bill nothing.
	failed := speechResponse{RequestID: strings.TrimSpace(decoded.TraceID)}
	providerCode := ""
	providerMessage := ""
	if decodeErr == nil && decoded.BaseResp != nil {
		providerCode = strings.TrimSpace(decoded.BaseResp.StatusCode.String())
		providerMessage = boundedProviderMessage(decoded.BaseResp.StatusMsg)
	}
	if statusCode < 200 || statusCode >= 300 {
		return failed, errSpeechProviderStatus{StatusCode: statusCode, Code: providerCode, Message: providerMessage}
	}
	if decodeErr != nil {
		return failed, fmt.Errorf("decode MiniMax T2A response: %w", decodeErr)
	}
	if providerCode != "" && providerCode != "0" {
		return failed, errSpeechProviderStatus{Code: providerCode, Message: providerMessage}
	}
	if decoded.Data == nil || strings.TrimSpace(decoded.Data.Audio) == "" {
		return failed, fmt.Errorf("MiniMax T2A response has no audio")
	}
	audio, err := hex.DecodeString(strings.TrimSpace(decoded.Data.Audio))
	if err != nil {
		return failed, fmt.Errorf("decode MiniMax hex audio: %w", err)
	}
	out := speechResponse{Audio: audio, RequestID: failed.RequestID}
	if decoded.ExtraInfo != nil {
		out.Characters = decoded.ExtraInfo.UsageCharacters
		out.DurationMs = decoded.ExtraInfo.AudioLength
	}
	return out, nil
}
