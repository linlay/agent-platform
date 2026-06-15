package contracts

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type SamplingSettings struct {
	Temperature      *float64
	TopP             *float64
	PresencePenalty  *float64
	FrequencyPenalty *float64
	Seed             *int64
}

func ParseSamplingSettings(raw map[string]any) SamplingSettings {
	if len(raw) == 0 {
		return SamplingSettings{}
	}
	return SamplingSettings{
		Temperature:      samplingFloatByKey(raw, "temperature"),
		TopP:             samplingFloatByKey(raw, "topP"),
		PresencePenalty:  samplingFloatByKey(raw, "presencePenalty"),
		FrequencyPenalty: samplingFloatByKey(raw, "frequencyPenalty"),
		Seed:             samplingInt64ByKey(raw, "seed"),
	}
}

func MergeSamplingSettings(base SamplingSettings, overlay SamplingSettings) SamplingSettings {
	out := base.Clone()
	if overlay.Temperature != nil {
		out.Temperature = cloneFloat64Ptr(overlay.Temperature)
	}
	if overlay.TopP != nil {
		out.TopP = cloneFloat64Ptr(overlay.TopP)
	}
	if overlay.PresencePenalty != nil {
		out.PresencePenalty = cloneFloat64Ptr(overlay.PresencePenalty)
	}
	if overlay.FrequencyPenalty != nil {
		out.FrequencyPenalty = cloneFloat64Ptr(overlay.FrequencyPenalty)
	}
	if overlay.Seed != nil {
		out.Seed = cloneInt64Ptr(overlay.Seed)
	}
	return out
}

func (s SamplingSettings) Clone() SamplingSettings {
	return SamplingSettings{
		Temperature:      cloneFloat64Ptr(s.Temperature),
		TopP:             cloneFloat64Ptr(s.TopP),
		PresencePenalty:  cloneFloat64Ptr(s.PresencePenalty),
		FrequencyPenalty: cloneFloat64Ptr(s.FrequencyPenalty),
		Seed:             cloneInt64Ptr(s.Seed),
	}
}

func (s SamplingSettings) IsZero() bool {
	return s.Temperature == nil &&
		s.TopP == nil &&
		s.PresencePenalty == nil &&
		s.FrequencyPenalty == nil &&
		s.Seed == nil
}

func (s SamplingSettings) ToMap() map[string]any {
	if s.IsZero() {
		return nil
	}
	out := map[string]any{}
	if s.Temperature != nil {
		out["temperature"] = *s.Temperature
	}
	if s.TopP != nil {
		out["topP"] = *s.TopP
	}
	if s.PresencePenalty != nil {
		out["presencePenalty"] = *s.PresencePenalty
	}
	if s.FrequencyPenalty != nil {
		out["frequencyPenalty"] = *s.FrequencyPenalty
	}
	if s.Seed != nil {
		out["seed"] = *s.Seed
	}
	return out
}

func ValidateSamplingSettings(raw any, fieldPath string) error {
	if raw == nil {
		return nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a map", fieldPath)
	}
	for rawKey, value := range node {
		switch normalizeSamplingKey(rawKey) {
		case "temperature", "topP", "presencePenalty", "frequencyPenalty":
			if _, ok := samplingFloatValue(value); !ok {
				return fmt.Errorf("%s.%s must be a number", fieldPath, rawKey)
			}
		case "seed":
			if _, ok := samplingInt64Value(value); !ok {
				return fmt.Errorf("%s.%s must be an integer", fieldPath, rawKey)
			}
		default:
			continue
		}
	}
	return nil
}

func samplingFloatByKey(raw map[string]any, canonical string) *float64 {
	for key, value := range raw {
		if normalizeSamplingKey(key) != canonical {
			continue
		}
		if parsed, ok := samplingFloatValue(value); ok {
			return &parsed
		}
	}
	return nil
}

func samplingInt64ByKey(raw map[string]any, canonical string) *int64 {
	for key, value := range raw {
		if normalizeSamplingKey(key) != canonical {
			continue
		}
		if parsed, ok := samplingInt64Value(value); ok {
			return &parsed
		}
	}
	return nil
}

func normalizeSamplingKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	switch key {
	case "temperature":
		return "temperature"
	case "topp":
		return "topP"
	case "presencepenalty":
		return "presencePenalty"
	case "frequencypenalty":
		return "frequencyPenalty"
	case "seed":
		return "seed"
	default:
		return ""
	}
}

func samplingFloatValue(value any) (float64, bool) {
	var parsed float64
	switch v := value.(type) {
	case int:
		parsed = float64(v)
	case int64:
		parsed = float64(v)
	case float64:
		parsed = v
	case float32:
		parsed = float64(v)
	case json.Number:
		number, err := strconv.ParseFloat(v.String(), 64)
		if err != nil {
			return 0, false
		}
		parsed = number
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func samplingInt64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			return 0, false
		}
		return int64(v), true
	case float32:
		value := float64(v)
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		number, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

func cloneFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
