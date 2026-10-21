package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/modelusage"
)

var speechEmotions = []string{"happy", "sad", "angry", "fearful", "disgusted", "surprised", "calm", "fluent", "whisper"}

func (t *RuntimeToolExecutor) invokeSpeechGenerate(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	cfg := t.cfg.SpeechGenerate
	if !cfg.Enabled {
		return modelToolError("speech_generate_disabled", "speech_generate is disabled by configs/tools.yml", nil), nil
	}
	if t.models == nil {
		return modelToolError("speech_generate_model_registry_unavailable", "model registry is not configured for speech_generate", nil), nil
	}
	text := strings.TrimSpace(AnyStringNode(args["text"]))
	if text == "" {
		return modelToolError("speech_generate_text_required", "text is required", nil), nil
	}
	profileName := strings.TrimSpace(AnyStringNode(args["profile"]))
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		return modelToolError("speech_generate_profile_not_found", "speech_generate profile not found: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if profile.ModelKey == "" {
		return modelToolError("speech_generate_profile_model_missing", "speech_generate profile model-key is required: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if utf8.RuneCountInString(text) > profile.MaxTextChars {
		return modelToolError("speech_generate_text_too_long", fmt.Sprintf("text exceeds max-text-chars: %d", profile.MaxTextChars), map[string]any{"maxTextChars": profile.MaxTextChars}), nil
	}
	model, provider, err := t.models.GetTTS(profile.ModelKey)
	if err != nil {
		return modelToolError("speech_generate_model_invalid", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if strings.TrimSpace(provider.BaseURL) == "" || !provider.HasCredential() {
		return modelToolError("speech_generate_provider_config_invalid", "provider baseUrl and apiKey are required", map[string]any{"provider": provider.Key}), nil
	}
	protocol, ok := speechProtocols[model.Protocol]
	if !ok {
		return modelToolError("speech_generate_protocol_unsupported", "speech protocol is not implemented: "+model.Protocol, map[string]any{"modelKey": model.Key}), nil
	}

	req, result, handled := speechRequestFromArgs(args, text, profile.Voice, profile.Voices, profile.Format, profile.SampleRate)
	if handled {
		return result, nil
	}
	body, err := protocol.buildBody(model, req)
	if err != nil {
		var unsupported errSpeechParamUnsupported
		if errors.As(err, &unsupported) {
			return modelToolError("speech_generate_param_unsupported", err.Error(), map[string]any{"param": unsupported.Param, "protocol": unsupported.Protocol}), nil
		}
		return modelToolError("speech_generate_request_invalid", err.Error(), nil), nil
	}

	start := time.Now()
	// The router owns the tool deadline; a profile timeout can only shorten it.
	callCtx := ctx
	if profile.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(profile.Timeout)*time.Second)
		defer cancel()
	}
	diagnostics := map[string]any{"modelKey": model.Key, "profile": profileName}
	statusCode, data, headerRequestID, err := t.postSpeechRequest(callCtx, provider, model, body, profile.MaxAudioBytes)
	if err != nil {
		return modelToolError("speech_generate_model_request_failed", err.Error(), diagnostics), nil
	}
	decoded, parseErr := protocol.parseResponse(statusCode, data)
	requestID := FirstNonEmptyString(decoded.RequestID, headerRequestID)
	if requestID != "" {
		diagnostics["upstreamRequestId"] = requestID
	}
	// Characters reported on a response are billed even when the audio
	// cannot be materialized afterwards.
	withUsage := func(result ToolExecutionResult) ToolExecutionResult {
		modelusage.AttachCharacters(&result, model.Key, modelusage.OperationSpeech, decoded.Characters, requestID)
		return result
	}
	if parseErr != nil {
		code := "speech_generate_model_response_invalid"
		var status errSpeechProviderStatus
		if errors.As(parseErr, &status) {
			code = "speech_generate_model_request_failed"
			if status.StatusCode > 0 {
				diagnostics["statusCode"] = status.StatusCode
			}
		}
		return withUsage(modelToolError(code, parseErr.Error(), diagnostics)), nil
	}
	audio := decoded.Audio
	if decoded.AudioURL != "" {
		audio, err = t.downloadSpeechAudio(callCtx, decoded.AudioURL, profile.MaxAudioBytes)
		if err != nil {
			return withUsage(modelToolError("speech_generate_model_response_invalid", err.Error(), diagnostics)), nil
		}
	}
	if len(audio) == 0 || len(audio) > profile.MaxAudioBytes {
		return withUsage(modelToolError("speech_generate_model_response_invalid", fmt.Sprintf("provider audio exceeds %d bytes or is empty", profile.MaxAudioBytes), diagnostics)), nil
	}
	format, mimeType, ok := detectSpeechAudio(audio)
	if !ok {
		return withUsage(modelToolError("speech_generate_model_response_invalid", "provider response is not mp3, wav, flac or ogg audio", diagnostics)), nil
	}
	artifact, err := persistGeneratedChatArtifact(t.cfg.Paths.ChatsDir, execCtx, audio, generatedChatArtifact{
		Prefix: "speech_generate",
		Ext:    "." + format,
		Kind:   "audio",
		Noun:   "generated audio",
	})
	if err != nil {
		return withUsage(modelToolError("speech_generate_persist_failed", err.Error(), diagnostics)), nil
	}
	sum := sha256.Sum256(audio)
	audioPayload := map[string]any{
		"format":    format,
		"mimeType":  mimeType,
		"sizeBytes": len(audio),
		"sha256":    hex.EncodeToString(sum[:]),
	}
	if decoded.DurationMs > 0 {
		audioPayload["durationMs"] = decoded.DurationMs
	}
	for key, value := range artifact {
		audioPayload[key] = value
	}
	payload := map[string]any{
		"ok":         true,
		"profile":    profileName,
		"modelKey":   model.Key,
		"voice":      req.Voice,
		"audio":      audioPayload,
		"durationMs": time.Since(start).Milliseconds(),
	}
	if decoded.Characters > 0 {
		payload["characters"] = decoded.Characters
	}
	if requestID != "" {
		payload["upstreamRequestId"] = requestID
	}
	return withUsage(structuredResult(payload)), nil
}

// speechRequestFromArgs merges tool arguments over profile defaults. It
// validates only protocol-neutral shape; ranges are checked by the protocol.
func speechRequestFromArgs(args map[string]any, text string, defaultVoice string, voices []string, defaultFormat string, defaultSampleRate int) (speechRequest, ToolExecutionResult, bool) {
	req := speechRequest{
		Text:       text,
		Voice:      FirstNonEmptyString(args["voice"], defaultVoice),
		Language:   strings.TrimSpace(AnyStringNode(args["language"])),
		Emotion:    strings.ToLower(strings.TrimSpace(AnyStringNode(args["emotion"]))),
		Format:     strings.ToLower(FirstNonEmptyString(args["format"], defaultFormat)),
		SampleRate: defaultSampleRate,
	}
	req.Voice = strings.TrimSpace(req.Voice)
	if req.Voice == "" {
		return req, modelToolError("speech_generate_voice_required", "voice is required; set it in the profile or pass voice", nil), true
	}
	if len(voices) > 0 && !slices.Contains(voices, req.Voice) {
		return req, modelToolError("speech_generate_voice_not_allowed", "voice is not listed for this profile: "+req.Voice, map[string]any{"voices": voices}), true
	}
	if req.Emotion != "" && !slices.Contains(speechEmotions, req.Emotion) {
		return req, modelToolError("speech_generate_emotion_invalid", "emotion must be one of "+strings.Join(speechEmotions, ", "), nil), true
	}
	if value, present, ok := speechNumberArg(args, "speed"); present {
		if !ok {
			return req, modelToolError("speech_generate_speed_invalid", "speed must be a number", nil), true
		}
		req.Speed = &value
	}
	if value, present, ok := speechNumberArg(args, "volume"); present {
		if !ok {
			return req, modelToolError("speech_generate_volume_invalid", "volume must be a number", nil), true
		}
		req.Volume = &value
	}
	if value, present, ok := speechNumberArg(args, "pitch"); present {
		if !ok || value != math.Trunc(value) {
			return req, modelToolError("speech_generate_pitch_invalid", "pitch must be an integer", nil), true
		}
		pitch := int(value)
		req.Pitch = &pitch
	}
	if value, present, ok := speechNumberArg(args, "sampleRate"); present {
		if !ok || value <= 0 || value != math.Trunc(value) {
			return req, modelToolError("speech_generate_sample_rate_invalid", "sampleRate must be a positive integer", nil), true
		}
		req.SampleRate = int(value)
	}
	return req, ToolExecutionResult{}, false
}

func speechNumberArg(args map[string]any, key string) (float64, bool, bool) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return 0, false, false
	}
	switch raw.(type) {
	case float64, float32, int, int64:
		return floatArg(args, key), true, true
	default:
		return 0, true, false
	}
}
