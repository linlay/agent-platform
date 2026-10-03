package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type confirmationRule struct {
	when        map[string]any
	viewportKey string
}

// Rules select presentation only; the business planner remains responsible for
// requiring approval and preparing a frozen review. No match never grants access.
func parseConfirmationRules(raw any) ([]confirmationRule, error) {
	if raw == nil {
		return nil, fmt.Errorf("confirmationRules must be an array")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("confirmationRules must be an array")
	}
	rules := make([]confirmationRule, 0, len(items))
	defaults := 0
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("confirmationRules[%d] must be an object", i)
		}
		for k := range m {
			if k != "when" && k != "viewportType" && k != "viewportKey" {
				return nil, fmt.Errorf("confirmationRules[%d]: unknown field %s", i, k)
			}
		}
		kind, _ := m["viewportType"].(string)
		key, _ := m["viewportKey"].(string)
		if kind != "html" || strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) || strings.ContainsAny(key, "/\\") || key == "." || key == ".." {
			return nil, fmt.Errorf("confirmationRules[%d] requires viewportType html and a valid viewportKey", i)
		}
		rule := confirmationRule{viewportKey: key}
		if v, exists := m["when"]; exists {
			when, ok := v.(map[string]any)
			if !ok || len(when) == 0 {
				return nil, fmt.Errorf("confirmationRules[%d].when must be a nonempty object; omit it for a default", i)
			}
			for pointer, expected := range when {
				if _, err := confirmationPointerTokens(pointer); err != nil {
					return nil, err
				}
				switch expected.(type) {
				case nil, string, bool, int, int64, float64, json.Number:
				default:
					return nil, fmt.Errorf("confirmationRules[%d].when values must be JSON scalars", i)
				}
			}
			rule.when = when
		} else {
			defaults++
		}
		rules = append(rules, rule)
	}
	if defaults > 1 {
		return nil, fmt.Errorf("confirmationRules permits only one default rule")
	}
	return rules, nil
}

func selectConfirmationRule(raw any, args map[string]any) (*confirmationRule, error) {
	if raw == nil {
		return nil, nil
	}
	rules, err := parseConfirmationRules(raw)
	if err != nil {
		return nil, err
	}
	var matched, fallback *confirmationRule
	for i := range rules {
		rule := &rules[i]
		if rule.when == nil {
			fallback = rule
			continue
		}
		matches := true
		for pointer, expected := range rule.when {
			actual, exists := confirmationPointerValue(args, pointer)
			a, ae := json.Marshal(actual)
			b, be := json.Marshal(expected)
			if !exists || ae != nil || be != nil || !bytes.Equal(a, b) {
				matches = false
				break
			}
		}
		if matches {
			if matched != nil {
				return nil, fmt.Errorf("confirmationRules: multiple conditional rules match this invocation")
			}
			matched = rule
		}
	}
	if matched != nil {
		return matched, nil
	}
	return fallback, nil
}

func confirmationPointerTokens(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("confirmationRules: parameter path %q must be a JSON Pointer beginning with /", pointer)
	}
	tokens := strings.Split(pointer[1:], "/")
	for i, token := range tokens {
		for j := 0; j < len(token); j++ {
			if token[j] == '~' {
				if j+1 == len(token) || (token[j+1] != '0' && token[j+1] != '1') {
					return nil, fmt.Errorf("confirmationRules: invalid JSON Pointer %q", pointer)
				}
				j++
			}
		}
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens, nil
}

func confirmationPointerValue(args map[string]any, pointer string) (any, bool) {
	tokens, err := confirmationPointerTokens(pointer)
	if err != nil {
		return nil, false
	}
	var current any = args
	for _, token := range tokens {
		switch value := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = value[token]
			if !exists {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) || strconv.Itoa(index) != token {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}
