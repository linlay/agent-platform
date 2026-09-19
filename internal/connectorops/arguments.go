package connectorops

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Parameter bindings append argv values directly; no shell interpolation.
type TextField struct {
	Source  string `json:"source"`
	Pattern string `json:"pattern"`
}

type CLIParameter struct {
	Field   string `json:"field"`
	Flag    string `json:"flag"`
	Repeat  bool   `json:"repeat,omitempty"`
	ReadSQL bool   `json:"readSQL,omitempty"`
}

func cliEntryForOS(op CLI, goos string) string {
	if goos == "windows" && op.EntryWindows != "" {
		return op.EntryWindows
	}
	return op.Entry
}

var flagPattern = regexp.MustCompile(`^--[a-z][a-z0-9-]*$`)

// Conservative subset: reject comments, multiple statements, file access and
// mutation tokens even inside literals. Unsupported queries fail before launch.
var forbiddenSQL = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|create|replace|truncate|grant|revoke|into|outfile|dumpfile|load_file|sleep|benchmark|call|execute|attach|pragma)\b`)

func readSQL(value string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "SELECT ") &&
		!strings.ContainsAny(value, ";\x00") && !strings.Contains(value, "--") &&
		!strings.Contains(value, "/*") && !strings.Contains(value, "#") && !forbiddenSQL.MatchString(value)
}
func validateCLI(op CLI) error {
	if op.JSONFlag != "" && (op.JSONFlag != "--json" || len(op.Parameters) != 0) {
		return fmt.Errorf("invalid JSON binding")
	}
	if len(op.Parameters) > 16 || len(op.DropFields) > 32 {
		return fmt.Errorf("too many CLI bindings")
	}
	for _, p := range op.TextFields {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return err
		}
	}
	for _, p := range op.Parameters {
		if p.Field == "" || !flagPattern.MatchString(p.Flag) {
			return fmt.Errorf("invalid parameter binding")
		}
	}
	return nil
}
func cliArguments(op CLI, encoded []byte) ([]string, error) {
	argv := append([]string{}, op.Args...)
	if op.JSONFlag != "" {
		return append(argv, op.JSONFlag, string(encoded)), nil
	}
	var args map[string]any
	if json.Unmarshal(encoded, &args) != nil {
		return nil, failure("invalid_arguments", 400)
	}
	for _, p := range op.Parameters {
		value, ok := args[p.Field]
		if !ok {
			continue
		}
		values := []any{value}
		if p.Repeat {
			var yes bool
			values, yes = value.([]any)
			if !yes {
				return nil, failure("invalid_arguments", 400)
			}
		}
		for _, v := range values {
			str, ok := v.(string)
			if !ok || strings.ContainsRune(str, 0) || (p.ReadSQL && !readSQL(str)) {
				return nil, failure("invalid_arguments", 400)
			}
			argv = append(argv, p.Flag, str)
		}
	}
	return argv, nil
}

func projectOutput(op CLI, output map[string]any) (map[string]any, error) {
	for _, field := range op.TextFields {
		value, ok := output[field.Source].(string)
		if !ok {
			return nil, failure("invalid_upstream_response", 502)
		}
		re, err := regexp.Compile(field.Pattern)
		if err != nil {
			return nil, failure("connector_unavailable", 503)
		}
		match := re.FindStringSubmatch(value)
		if match == nil {
			return nil, failure("invalid_upstream_response", 502)
		}
		for i, name := range re.SubexpNames() {
			if i > 0 && name != "" {
				output[name] = strings.TrimSpace(match[i])
			}
		}
	}
	for _, key := range op.DropFields {
		delete(output, key)
	}
	return output, nil
}
