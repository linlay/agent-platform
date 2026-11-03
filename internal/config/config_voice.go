package config

import (
	"fmt"
	"strings"
)

type VoiceConfig struct {
	Enabled              bool
	ASRModel             string
	TTSModel             string
	DefaultVoice         string
	Voices               []string
	MaxSentenceSilenceMS int
	DownlinkLeadMS       int
}

func (c *Config) applyVoice(raw any) error {
	v, err := configMap(raw, "voice", "enabled", "asr-model", "tts-model", "default-voice", "voices", "max-sentence-silence-ms", "downlink-lead-ms")
	if err != nil {
		return err
	}
	next := c.Voice
	if raw, exists := v["enabled"]; exists {
		value, ok := raw.(bool)
		if !ok {
			return fmt.Errorf("voice.enabled must be a boolean")
		}
		next.Enabled = value
	}
	for key, dest := range map[string]*string{"asr-model": &next.ASRModel, "tts-model": &next.TTSModel, "default-voice": &next.DefaultVoice} {
		if raw, exists := v[key]; exists {
			value, ok := raw.(string)
			if !ok {
				return fmt.Errorf("voice.%s must be a string", key)
			}
			*dest = strings.TrimSpace(value)
		}
	}
	for _, field := range []struct {
		key      string
		dest     *int
		min, max int64
	}{{"max-sentence-silence-ms", &next.MaxSentenceSilenceMS, 200, 6000}, {"downlink-lead-ms", &next.DownlinkLeadMS, 0, 1000}} {
		if raw, exists := v[field.key]; exists {
			n, ok := raw.(int64)
			if !ok || n < field.min || n > field.max {
				return fmt.Errorf("voice.%s must be an integer between %d and %d", field.key, field.min, field.max)
			}
			*field.dest = int(n)
		}
	}
	if raw, exists := v["voices"]; exists {
		items, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("voice.voices must be a list of strings")
		}
		next.Voices = nil
		seen := map[string]bool{}
		for _, item := range items {
			value, ok := item.(string)
			value = strings.TrimSpace(value)
			if !ok || value == "" || seen[value] {
				return fmt.Errorf("voice.voices must contain unique nonempty strings")
			}
			seen[value] = true
			next.Voices = append(next.Voices, value)
		}
	}
	if next.Enabled {
		if next.ASRModel == "" || next.TTSModel == "" || next.DefaultVoice == "" {
			return fmt.Errorf("enabled voice requires asr-model, tts-model and default-voice")
		}
		found := false
		for _, voice := range next.Voices {
			found = found || voice == next.DefaultVoice
		}
		if !found {
			return fmt.Errorf("voice.default-voice must be listed in voice.voices")
		}
	}
	c.Voice = next
	return nil
}
