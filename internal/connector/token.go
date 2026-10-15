package connector

import (
	"fmt"
	"regexp"
	"strings"
)

type TokenField struct {
	Key          string                       `json:"key"`
	Label        string                       `json:"label"`
	Type         string                       `json:"type,omitempty"`
	Required     bool                         `json:"required"`
	Placeholder  string                       `json:"placeholder,omitempty"`
	Description  string                       `json:"description,omitempty"`
	DefaultValue string                       `json:"defaultValue,omitempty"`
	I18N         map[string]TokenFieldStrings `json:"i18n,omitempty"`
}

type TokenFieldStrings struct {
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Description string `json:"description,omitempty"`
}

// TokenSchema is token-schema.json: the credential form of an authMode token
// connector. Values entered by the user never live in the package.
type TokenSchema struct {
	Title       string                        `json:"title,omitempty"`
	Description string                        `json:"description,omitempty"`
	DocLabel    string                        `json:"docLabel,omitempty"`
	DocURL      string                        `json:"docUrl,omitempty"`
	I18N        map[string]TokenSchemaStrings `json:"i18n,omitempty"`
	Fields      []TokenField                  `json:"fields"`
}

type TokenSchemaStrings struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	DocLabel    string `json:"docLabel,omitempty"`
}

var tokenFieldKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func TokenFields(manifest Manifest) ([]TokenField, error) {
	var schema TokenSchema
	if err := DecodeJSON(manifest.TokenSchema, &schema); err != nil || len(schema.Fields) == 0 {
		return nil, fmt.Errorf("token-schema.json requires non-empty fields")
	}
	seen := map[string]bool{}
	for i := range schema.Fields {
		f := &schema.Fields[i]
		if !tokenFieldKey.MatchString(f.Key) || seen[f.Key] {
			return nil, fmt.Errorf("token-schema.json field keys must be unique uppercase identifiers")
		}
		seen[f.Key] = true
		if f.Type == "" {
			f.Type = "password"
		}
		if f.Type != "text" && f.Type != "password" || f.Type == "password" && f.DefaultValue != "" {
			return nil, fmt.Errorf("token-schema.json field %s requires text/password and no password default", f.Key)
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
