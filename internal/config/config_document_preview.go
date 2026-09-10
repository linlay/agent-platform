package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (c *Config) applyDocumentPreview(value any) error {
	values, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("document-preview must be a mapping")
	}
	for key, value := range values {
		switch key {
		case "enabled":
			v, ok := value.(bool)
			if !ok {
				return fmt.Errorf("document-preview enabled must be boolean")
			}
			c.DocumentPreview.Enabled = v
		case "provider", "api-base-url", "public-base-url", "open-mode":
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("document-preview %s must be a string", key)
			}
			v = strings.TrimSpace(v)
			switch key {
			case "provider":
				c.DocumentPreview.Provider = v
			case "api-base-url":
				c.DocumentPreview.APIBaseURL = v
			case "public-base-url":
				c.DocumentPreview.PublicBaseURL = v
			case "open-mode":
				c.DocumentPreview.OpenMode = v
			}
		case "max-file-bytes":
			v, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
			if err != nil {
				return fmt.Errorf("document-preview max-file-bytes must be integer")
			}
			c.DocumentPreview.MaxFileBytes = v
		case "request-timeout":
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("document-preview request-timeout must be duration")
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("document-preview request-timeout is invalid")
			}
			c.DocumentPreview.RequestTimeout = d
		case "auth":
			auth, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("document-preview auth must be a mapping")
			}
			for field, raw := range auth {
				v, ok := raw.(string)
				if !ok {
					return fmt.Errorf("document-preview auth fields must be strings")
				}
				switch field {
				case "mode":
					c.DocumentPreview.AuthMode = strings.TrimSpace(v)
				case "token-file":
					c.DocumentPreview.TokenFile = strings.TrimSpace(v)
				default:
					return fmt.Errorf("unknown document-preview auth field %q", field)
				}
			}
		default:
			return fmt.Errorf("unknown document-preview field %q", key)
		}
	}
	return c.DocumentPreview.Validate()
}
