package models

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	OpenAIStreamTerminationFinishReason = "finish-reason"
	OpenAIStreamTerminationStreamEnd    = "stream-end"

	OpenAITrailingTimeoutDefaultMS = 2000
	OpenAITrailingTimeoutMinMS     = 1
	OpenAITrailingTimeoutMaxMS     = 10000

	OpenAICacheMissDerivePromptMinusCacheHit = "prompt-minus-cache-hit"
	openAILegacyCacheMissDerive              = "promptTokensMinusCacheHitTokens"
)

// OpenAIResponseCompat is the validated runtime view of compat.response.
// Unknown compat fields remain opaque and continue to be handled by their
// existing protocol-specific readers.
type OpenAIResponseCompat struct {
	StreamTermination     string
	TrailingTimeoutMS     int
	PromptCacheMissDerive string
}

// ParseOpenAIResponseCompat validates the OpenAI-compatible response contract
// used by both registry loading and admin catalog validation.
func ParseOpenAIResponseCompat(raw any) (OpenAIResponseCompat, error) {
	parsed := OpenAIResponseCompat{
		StreamTermination: OpenAIStreamTerminationFinishReason,
		TrailingTimeoutMS: OpenAITrailingTimeoutDefaultMS,
	}
	compat, present, err := optionalCompatMap(raw, "compat")
	if err != nil || !present {
		return parsed, err
	}
	response, present, err := optionalCompatChildMap(compat, "response", "compat.response")
	if err != nil || !present {
		return parsed, err
	}
	stream, streamPresent, err := optionalCompatChildMap(response, "stream", "compat.response.stream")
	if err != nil {
		return parsed, err
	}
	if streamPresent {
		if value, exists := stream["termination"]; exists && value != nil {
			termination, ok := value.(string)
			if !ok {
				return parsed, fmt.Errorf("compat.response.stream.termination must be a string")
			}
			termination = strings.ToLower(strings.TrimSpace(termination))
			switch termination {
			case OpenAIStreamTerminationFinishReason, OpenAIStreamTerminationStreamEnd:
				parsed.StreamTermination = termination
			default:
				return parsed, fmt.Errorf("compat.response.stream.termination must be %q or %q", OpenAIStreamTerminationFinishReason, OpenAIStreamTerminationStreamEnd)
			}
		}
		if value, exists := stream["trailingTimeoutMs"]; exists && value != nil {
			timeout, ok := strictCompatInt(value)
			if !ok {
				return parsed, fmt.Errorf("compat.response.stream.trailingTimeoutMs must be an integer")
			}
			if timeout < OpenAITrailingTimeoutMinMS || timeout > OpenAITrailingTimeoutMaxMS {
				return parsed, fmt.Errorf("compat.response.stream.trailingTimeoutMs must be between %d and %d", OpenAITrailingTimeoutMinMS, OpenAITrailingTimeoutMaxMS)
			}
			parsed.TrailingTimeoutMS = timeout
		}
	}

	usage, usagePresent, err := optionalCompatChildMap(response, "usage", "compat.response.usage")
	if err != nil || !usagePresent {
		return parsed, err
	}
	promptDetails, detailsPresent, err := optionalCompatChildMap(usage, "promptTokensDetails", "compat.response.usage.promptTokensDetails")
	if err != nil || !detailsPresent {
		return parsed, err
	}
	if err := validateOpenAIUsagePathRule(promptDetails, "cacheHitTokens"); err != nil {
		return parsed, err
	}
	if err := validateOpenAIUsagePathRule(promptDetails, "cacheMissTokens"); err != nil {
		return parsed, err
	}
	completionDetails, completionPresent, err := optionalCompatChildMap(usage, "completionTokensDetails", "compat.response.usage.completionTokensDetails")
	if err != nil {
		return parsed, err
	}
	if completionPresent {
		if err := validateOpenAIUsageRulePath(completionDetails, "reasoningTokens", "compat.response.usage.completionTokensDetails.reasoningTokens"); err != nil {
			return parsed, err
		}
	}
	cacheMiss, cacheMissPresent, err := optionalCompatChildMap(promptDetails, "cacheMissTokens", "compat.response.usage.promptTokensDetails.cacheMissTokens")
	if err != nil || !cacheMissPresent {
		return parsed, err
	}
	if value, exists := cacheMiss["derive"]; exists && value != nil {
		derive, ok := value.(string)
		if !ok {
			return parsed, fmt.Errorf("compat.response.usage.promptTokensDetails.cacheMissTokens.derive must be a string")
		}
		derive = strings.TrimSpace(derive)
		switch derive {
		case OpenAICacheMissDerivePromptMinusCacheHit, openAILegacyCacheMissDerive:
			parsed.PromptCacheMissDerive = OpenAICacheMissDerivePromptMinusCacheHit
		default:
			return parsed, fmt.Errorf("compat.response.usage.promptTokensDetails.cacheMissTokens.derive must be %q", OpenAICacheMissDerivePromptMinusCacheHit)
		}
	}
	return parsed, nil
}

func optionalCompatMap(raw any, path string) (map[string]any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s must be a map", path)
	}
	return value, true, nil
}

func optionalCompatChildMap(parent map[string]any, key string, path string) (map[string]any, bool, error) {
	raw, exists := parent[key]
	if !exists || raw == nil {
		return nil, false, nil
	}
	return optionalCompatMap(raw, path)
}

func validateOpenAIUsagePathRule(parent map[string]any, key string) error {
	path := "compat.response.usage.promptTokensDetails." + key
	return validateOpenAIUsageRulePath(parent, key, path)
}

func validateOpenAIUsageRulePath(parent map[string]any, key string, path string) error {
	rule, present, err := optionalCompatChildMap(parent, key, path)
	if err != nil || !present {
		return err
	}
	value, exists := rule["path"]
	if !exists || value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s.path must be a non-empty string or null", path)
	}
	for _, segment := range strings.Split(text, ".") {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("%s.path must not contain empty segments", path)
		}
	}
	return nil
}

func strictCompatInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		if uint64(typed) > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		if typed > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(typed), true
	case float32:
		value := float64(typed)
		if math.Trunc(value) != value {
			return 0, false
		}
		return int(value), true
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		return int(parsed), err == nil
	default:
		return 0, false
	}
}
