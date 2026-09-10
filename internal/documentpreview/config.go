// Package documentpreview prepares readonly Office previews without a Run.
package documentpreview

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Enabled        bool
	Provider       string
	APIBaseURL     string
	PublicBaseURL  string
	AuthMode       string
	TokenFile      string
	OpenMode       string
	MaxFileBytes   int64
	RequestTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{Provider: "document-hub", APIBaseURL: "http://127.0.0.1:8090", PublicBaseURL: "http://127.0.0.1:8090", AuthMode: "none", OpenMode: "iframe", MaxFileBytes: 50 << 20, RequestTimeout: 120 * time.Second}
}

func (c Config) Validate() error {
	if c.Provider != "document-hub" {
		return fmt.Errorf("document-preview provider must be document-hub")
	}
	if c.OpenMode != "iframe" && c.OpenMode != "external" {
		return fmt.Errorf("document-preview open-mode must be iframe or external")
	}
	if c.AuthMode != "none" && c.AuthMode != "bearer-token-file" {
		return fmt.Errorf("document-preview auth mode is invalid")
	}
	if c.AuthMode == "bearer-token-file" && !filepath.IsAbs(c.TokenFile) {
		return fmt.Errorf("document-preview token-file must be absolute")
	}
	if c.AuthMode == "none" && c.TokenFile != "" {
		return fmt.Errorf("document-preview token-file requires bearer-token-file mode")
	}
	if c.MaxFileBytes <= 0 || c.MaxFileBytes > 200<<20 {
		return fmt.Errorf("document-preview max-file-bytes must be between 1 and 209715200")
	}
	if c.RequestTimeout <= 0 || c.RequestTimeout > 10*time.Minute {
		return fmt.Errorf("document-preview request-timeout must be between 0 and 10m")
	}
	for _, raw := range []string{c.APIBaseURL, c.PublicBaseURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("document-preview URLs must be HTTP(S) origins without credentials, paths or queries")
		}
	}
	return nil
}

func (c Config) Capabilities() Capabilities {
	return Capabilities{Enabled: c.Enabled, SupportedExtensions: []string{"docx", "pptx", "xlsx"}, MaxFileBytes: c.MaxFileBytes, OpenMode: c.OpenMode}
}

func (c Config) normalized() Config {
	c.APIBaseURL = strings.TrimRight(c.APIBaseURL, "/")
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	return c
}
