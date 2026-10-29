package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CLIArgs builds argv from the declared items. Values are passed as separate
// argv entries, never through a shell. A missing or null param drops its item.
func CLIArgs(items []QueryArg, params map[string]any) ([]string, error) {
	argv := make([]string, 0, len(items))
	for _, item := range items {
		if item.Param == "" {
			argv = append(argv, item.Literal)
			continue
		}
		value, present := params[item.Param]
		if !present || value == nil {
			continue
		}
		if item.Flag != "" {
			enabled, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: params.%s must be a boolean", ErrDataInvalid, item.Param)
			}
			if enabled {
				argv = append(argv, item.Flag)
			}
			continue
		}
		text, err := argText(item, value)
		if err != nil {
			return nil, err
		}
		if item.Opt != "" {
			argv = append(argv, item.Opt, text)
			continue
		}
		// A positional value must not be read as an option.
		if strings.HasPrefix(text, "-") {
			return nil, fmt.Errorf("%w: params.%s must not start with '-'", ErrDataInvalid, item.Param)
		}
		argv = append(argv, text)
	}
	return argv, nil
}

func argText(item QueryArg, value any) (string, error) {
	if !item.JSON {
		return scalarText(item.Param, value)
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		return "", fmt.Errorf("%w: params.%s must be an object or array", ErrDataInvalid, item.Param)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: params.%s: %v", ErrDataInvalid, item.Param, err)
	}
	return string(raw), nil
}

func scalarText(name string, value any) (string, error) {
	switch v := value.(type) {
	case string:
		if strings.ContainsRune(v, 0) {
			return "", fmt.Errorf("%w: params.%s contains NUL", ErrDataInvalid, name)
		}
		return v, nil
	case json.Number:
		return v.String(), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("%w: params.%s must be a string, number or boolean", ErrDataInvalid, name)
}

// InterceptPath is the subcommand path of a template: its literals that do
// not start with '-', in order.
func InterceptPath(items []QueryArg) []string {
	var path []string
	for _, item := range items {
		if item.Param == "" && !strings.HasPrefix(item.Literal, "-") {
			path = append(path, item.Literal)
		}
	}
	return path
}

// ContainsPath reports whether path appears as consecutive argv items.
func ContainsPath(argv, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for start := 0; start+len(path) <= len(argv); start++ {
		match := true
		for i, token := range path {
			if argv[start+i] != token {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ParseCLIArgs is the inverse of CLIArgs: it reads params back from argv.
// Literal options may appear anywhere and may be omitted (CLIArgs adds them
// again); other literals and positional params keep template order. Any token
// the template cannot place fails, so a differently spelled call cannot pass
// as the declared one. The caller validates the result against the schema.
func ParseCLIArgs(items []QueryArg, schema json.RawMessage, argv []string) (map[string]any, error) {
	types := propertyTypes(schema)
	options := map[string]QueryArg{}
	fixed := map[string]bool{}
	var sequence []QueryArg
	for _, item := range items {
		switch {
		case item.Param == "" && strings.HasPrefix(item.Literal, "-"):
			fixed[item.Literal] = true
		case item.Opt != "":
			options[item.Opt] = item
		case item.Flag != "":
			options[item.Flag] = item
		default:
			sequence = append(sequence, item)
		}
	}
	params := map[string]any{}
	set := func(name string, value any) error {
		if _, exists := params[name]; exists {
			return fmt.Errorf("%w: %s is given more than once", ErrDataInvalid, name)
		}
		params[name] = value
		return nil
	}
	next := 0
	for i := 0; i < len(argv); i++ {
		token := argv[i]
		if len(token) > 1 && strings.HasPrefix(token, "-") {
			name, inline, hasInline := token, "", false
			if strings.HasPrefix(token, "--") {
				name, inline, hasInline = strings.Cut(token, "=")
			}
			item, known := options[name]
			switch {
			case known && item.Flag != "":
				if hasInline {
					return nil, fmt.Errorf("%w: flag %s takes no value", ErrDataInvalid, name)
				}
				if err := set(item.Param, true); err != nil {
					return nil, err
				}
			case known:
				if !hasInline {
					if i+1 >= len(argv) {
						return nil, fmt.Errorf("%w: option %s needs a value", ErrDataInvalid, name)
					}
					i++
					inline = argv[i]
				}
				value, err := argValue(item, types[item.Param], inline)
				if err != nil {
					return nil, err
				}
				if err := set(item.Param, value); err != nil {
					return nil, err
				}
			case fixed[token]:
			default:
				return nil, fmt.Errorf("%w: option %s is not accepted", ErrDataInvalid, name)
			}
			continue
		}
		if next >= len(sequence) {
			return nil, fmt.Errorf("%w: argument %q is not accepted", ErrDataInvalid, token)
		}
		item := sequence[next]
		next++
		if item.Param == "" {
			if token != item.Literal {
				return nil, fmt.Errorf("%w: expected %q, got %q", ErrDataInvalid, item.Literal, token)
			}
			continue
		}
		value, err := argValue(item, types[item.Param], token)
		if err != nil {
			return nil, err
		}
		if err := set(item.Param, value); err != nil {
			return nil, err
		}
	}
	for ; next < len(sequence); next++ {
		if sequence[next].Param == "" {
			return nil, fmt.Errorf("%w: missing %q", ErrDataInvalid, sequence[next].Literal)
		}
	}
	return params, nil
}

// argValue converts one argv value by its JSON arg flag or schema type.
func argValue(item QueryArg, typ, text string) (any, error) {
	if item.JSON || typ == "integer" || typ == "number" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil || decoder.More() {
			return nil, fmt.Errorf("%w: %s is not valid JSON", ErrDataInvalid, item.Param)
		}
		switch value.(type) {
		case map[string]any, []any:
			if item.JSON {
				return value, nil
			}
		case json.Number:
			if !item.JSON {
				return value, nil
			}
		}
		return nil, fmt.Errorf("%w: %s has the wrong type", ErrDataInvalid, item.Param)
	}
	if typ == "boolean" {
		switch text {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, fmt.Errorf("%w: %s must be true or false", ErrDataInvalid, item.Param)
	}
	return text, nil
}

// propertyTypes maps each schema property to its first non-null type.
func propertyTypes(schema json.RawMessage) map[string]string {
	var parsed struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	types := map[string]string{}
	if len(schema) == 0 || json.Unmarshal(schema, &parsed) != nil {
		return types
	}
	for name, property := range parsed.Properties {
		var single string
		var several []string
		if json.Unmarshal(property.Type, &single) == nil {
			types[name] = single
			continue
		}
		if json.Unmarshal(property.Type, &several) == nil {
			for _, typ := range several {
				if typ != "null" {
					types[name] = typ
					break
				}
			}
		}
	}
	return types
}
