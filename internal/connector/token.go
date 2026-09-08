package connector

import (
	"fmt"
	"regexp"
	"strings"
)

type TokenField struct {
	Key          string `json:"key,omitempty"`
	Name         string `json:"name,omitempty"` // Older packages used name for key.
	Label        string `json:"label"`
	Type         string `json:"type,omitempty"`
	Required     bool   `json:"required"`
	Placeholder  string `json:"placeholder,omitempty"`
	Description  string `json:"description,omitempty"`
	DefaultValue string `json:"defaultValue,omitempty"`
}

func TokenFields(manifest Manifest) ([]TokenField, error) {
	var schema struct {
		Fields      []TokenField `json:"fields"`
		Title       string       `json:"title,omitempty"`
		Description string       `json:"description,omitempty"`
		DocURL      string       `json:"docUrl,omitempty"`
		DocLabel    string       `json:"docLabel,omitempty"`
	}
	if err := DecodeJSON(manifest.TokenSchema, &schema); err != nil || len(schema.Fields) == 0 {
		return nil, fmt.Errorf("token_schema requires non-empty fields")
	}
	seen := map[string]bool{}
	for i := range schema.Fields {
		f := &schema.Fields[i]
		if f.Key == "" {
			f.Key = f.Name
		}
		if !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(f.Key) || seen[f.Key] || f.Name != "" && f.Name != f.Key {
			return nil, fmt.Errorf("token_schema field keys must be unique uppercase identifiers")
		}
		seen[f.Key] = true
		if f.Type == "" {
			f.Type = "password"
		}
		if f.Type != "text" && f.Type != "password" || f.Type == "password" && f.DefaultValue != "" {
			return nil, fmt.Errorf("token_schema field %s requires text/password and no password default", f.Key)
		}
	}
	return schema.Fields, nil
}

// ValidateTokenValues never includes credential values in diagnostics.
func ValidateTokenValues(manifest Manifest, values map[string]string) error {
	fields, err := TokenFields(manifest)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, field := range fields {
		known[field.Key] = true
		value := values[field.Key]
		if field.Required && strings.TrimSpace(value) == "" {
			return fmt.Errorf("credential field %s is required", field.Key)
		}
		if len(value) > 64*1024 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("credential field %s must be a single line of at most 64 KiB", field.Key)
		}
	}
	for key := range values {
		if !known[key] {
			return fmt.Errorf("credentials contain an undeclared field")
		}
	}
	return nil
}
