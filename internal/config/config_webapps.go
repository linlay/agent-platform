package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

type WebAppHostMode string

const (
	WebAppHostDisabled WebAppHostMode = "disabled"
	WebAppHostCompose  WebAppHostMode = "compose"
)

func ParseWebAppHostMode(value string) (WebAppHostMode, error) {
	switch WebAppHostMode(strings.TrimSpace(value)) {
	case "", WebAppHostDisabled:
		return WebAppHostDisabled, nil
	case WebAppHostCompose:
		return WebAppHostCompose, nil
	default:
		return "", fmt.Errorf("webapp-host must be disabled or compose")
	}
}

// WebAppsConfig is deployment policy, never application-controlled settings.
// JSON tags match the strict runtime YAML keys; public DTOs are separate.
type WebAppsConfig struct {
	Enabled         bool                `json:"enabled"`
	PlatformOrigin  string              `json:"platform-origin"`
	ContentDomain   string              `json:"content-domain"`
	InstanceSlug    string              `json:"instance-slug"`
	MaxRunning      int                 `json:"max-running"`
	MaxArchiveMiB   int                 `json:"max-archive-mib"`
	MaxExtractedMiB int                 `json:"max-extracted-mib"`
	MaxFiles        int                 `json:"max-files"`
	Backend         WebAppBackendConfig `json:"backend"`
	Logs            WebAppLogConfig     `json:"logs"`
}

type WebAppBackendConfig struct {
	PortStart             int                 `json:"port-start"`
	PortEnd               int                 `json:"port-end"`
	MaxOldSpaceSizeMB     int                 `json:"max-old-space-size-mb"`
	RSSStopMB             int                 `json:"rss-stop-mb"`
	HealthIntervalSeconds int                 `json:"health-interval-seconds"`
	UnhealthyThreshold    int                 `json:"unhealthy-threshold"`
	Restart               WebAppRestartConfig `json:"restart"`
}

type WebAppRestartConfig struct {
	MaxAttempts        int `json:"max-attempts"`
	WindowSeconds      int `json:"window-seconds"`
	StableResetSeconds int `json:"stable-reset-seconds"`
}

type WebAppLogConfig struct {
	MaxSizeMB int `json:"max-size-mb"`
	MaxFiles  int `json:"max-files"`
}

func DefaultWebAppsConfig() WebAppsConfig {
	return WebAppsConfig{
		MaxRunning: 4, MaxArchiveMiB: 100, MaxExtractedMiB: 512, MaxFiles: 20000,
		Backend: WebAppBackendConfig{
			PortStart: 20000, PortEnd: 20127, MaxOldSpaceSizeMB: 256, RSSStopMB: 512,
			HealthIntervalSeconds: 10, UnhealthyThreshold: 3,
			Restart: WebAppRestartConfig{MaxAttempts: 5, WindowSeconds: 600, StableResetSeconds: 300},
		}, Logs: WebAppLogConfig{MaxSizeMB: 10, MaxFiles: 3},
	}
}

func (c *Config) applyWebApps(raw any) error {
	if err := checkWebAppKeys(raw, reflect.TypeFor[WebAppsConfig](), "webapps"); err != nil {
		return err
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("webapps: %w", err)
	}
	// Decode into a copy so a malformed section cannot partially change policy.
	next := c.WebApps
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&next); err != nil {
		return fmt.Errorf("webapps: %w", err)
	}
	if err := next.validateLimits(); err != nil {
		return err
	}
	c.WebApps = next
	return nil
}

// encoding/json alone accepts case-insensitive keys and null primitives.
// Validate the original YAML shapes first, including nested exact key spelling.
func checkWebAppKeys(raw any, typ reflect.Type, path string) error {
	values, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be a mapping", path)
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fields[field.Tag.Get("json")] = field.Type
	}
	for k, v := range values {
		ft, ok := fields[k]
		if !ok {
			return fmt.Errorf("unknown %s.%s", path, k)
		}
		if v == nil {
			return fmt.Errorf("%s.%s must not be null", path, k)
		}
		switch ft.Kind() {
		case reflect.Bool:
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s.%s must be boolean", path, k)
			}
		case reflect.String:
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s.%s must be a string", path, k)
			}
		case reflect.Int:
			if _, ok := v.(int64); !ok {
				return fmt.Errorf("%s.%s must be an integer", path, k)
			}
		}
		if ft.Kind() == reflect.Struct {
			if err := checkWebAppKeys(v, ft, path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c WebAppsConfig) validateLimits() error {
	for _, value := range []struct {
		name   string
		n, max int
	}{
		{"max-running", c.MaxRunning, 128}, {"max-archive-mib", c.MaxArchiveMiB, 1024},
		{"max-extracted-mib", c.MaxExtractedMiB, 8192}, {"max-files", c.MaxFiles, 100000},
		{"backend.max-old-space-size-mb", c.Backend.MaxOldSpaceSizeMB, 65536},
		{"backend.rss-stop-mb", c.Backend.RSSStopMB, 131072},
		{"backend.health-interval-seconds", c.Backend.HealthIntervalSeconds, 300},
		{"backend.unhealthy-threshold", c.Backend.UnhealthyThreshold, 100},
		{"backend.restart.max-attempts", c.Backend.Restart.MaxAttempts, 100},
		{"backend.restart.window-seconds", c.Backend.Restart.WindowSeconds, 86400},
		{"backend.restart.stable-reset-seconds", c.Backend.Restart.StableResetSeconds, 86400},
		{"logs.max-size-mb", c.Logs.MaxSizeMB, 1024}, {"logs.max-files", c.Logs.MaxFiles, 20},
	} {
		if value.n < 1 || value.n > value.max {
			return fmt.Errorf("webapps.%s must be between 1 and %d", value.name, value.max)
		}
	}
	b := c.Backend
	if b.PortStart < 1024 || b.PortEnd > 65535 || b.PortEnd < b.PortStart || b.PortEnd-b.PortStart+1 < c.MaxRunning {
		return fmt.Errorf("webapps.backend port range must be unprivileged and contain max-running ports")
	}
	if b.RSSStopMB <= b.MaxOldSpaceSizeMB {
		return fmt.Errorf("webapps.backend.rss-stop-mb must exceed max-old-space-size-mb")
	}
	if c.MaxExtractedMiB < c.MaxArchiveMiB {
		return fmt.Errorf("webapps.max-extracted-mib must not be smaller than max-archive-mib")
	}
	return nil
}

var webAppDNSLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validWebAppDomain(value string) bool {
	if value != strings.ToLower(value) || len(value) > 253 || net.ParseIP(value) != nil {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !webAppDNSLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func (c *Config) validateWebApps(goos string) error {
	if !c.WebApps.Enabled {
		return nil
	}
	if goos != "linux" || c.RuntimeMode != RuntimeModeStandalone || c.WebAppHost != WebAppHostCompose {
		return fmt.Errorf("webapps.enabled requires Linux standalone with --webapp-host=compose")
	}
	if err := c.WebApps.validateLimits(); err != nil {
		return err
	}
	if !c.Auth.Enabled || !c.Auth.RequireExpiry || strings.TrimSpace(c.Auth.Issuer) == "" || strings.TrimSpace(c.Auth.Audience) == "" || len(c.Auth.AllowedSubjects) != 1 || strings.TrimSpace(c.Auth.AllowedSubjects[0]) == "" {
		return fmt.Errorf("webapps requires auth.enabled, issuer, audience, require-expiry and exactly one allowed-subjects owner")
	}
	w := c.WebApps
	u, err := url.Parse(w.PlatformOrigin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" || !validWebAppDomain(u.Hostname()) || u.Host != u.Hostname() {
		return fmt.Errorf("webapps.platform-origin must be a canonical HTTPS origin with no port, path, userinfo, query or fragment")
	}
	if !validWebAppDomain(w.ContentDomain) {
		return fmt.Errorf("webapps.content-domain must be a lowercase DNS domain")
	}
	platformSite, pErr := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
	contentSite, cErr := publicsuffix.EffectiveTLDPlusOne(w.ContentDomain)
	if pErr != nil || cErr != nil || platformSite == contentSite || contentSite == w.ContentDomain {
		return fmt.Errorf("webapps.content-domain must be a subdomain on a separate registrable domain from platform-origin")
	}
	if !webAppDNSLabel.MatchString(w.InstanceSlug) || len(w.InstanceSlug) > 45 {
		return fmt.Errorf("webapps.instance-slug must be a lowercase DNS label of at most 45 characters")
	}
	if len(w.InstanceSlug)+2+16+1+len(w.ContentDomain) > 253 {
		return fmt.Errorf("webapps application hostname is too long")
	}
	if c.Server.PublicBaseURL != "" && c.Server.PublicBaseURL != w.PlatformOrigin {
		return fmt.Errorf("webapps.platform-origin must match resource.public-base-url when set")
	}
	for _, name := range []string{c.WebAppsCodeDir(), c.WebAppsDataDir(), c.WebAppsStateDir()} {
		if !filepath.IsAbs(name) {
			return fmt.Errorf("webapps runtime paths must be absolute after configuration normalization")
		}
	}
	return nil
}

func (c Config) WebAppsCodeDir() string { return c.Paths.WebAppsDir }
func (c Config) WebAppsDataDir() string { return c.Paths.WebAppDataDir }
func (c Config) WebAppsStateDir() string {
	return filepath.Join(c.Paths.EffectiveStateDir(), "webapps")
}
