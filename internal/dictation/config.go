// Package dictation owns standalone transcription, never conversation execution.
package dictation

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/models"
	"agent-platform/internal/speech"
)

const MaxRecordingSeconds = 60

type Config struct {
	Listen         string            `json:"listen"`
	ModelFile      string            `json:"modelFile"`
	ProviderFile   string            `json:"providerFile"`
	SsoIdTokenFile string            `json:"ssoIdTokenFile"`
	Auth           config.AuthConfig `json:"auth"`
	HTTPProxy      httpclient.Config `json:"httpProxy"`
	AllowedOrigins []string          `json:"allowedOrigins"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:11964", Auth: config.AuthConfig{Enabled: true, RequireExpiry: true}, HTTPProxy: httpclient.Config{Mode: "auto", SystemRefreshInterval: httpclient.DefaultRefreshInterval}}
	if _, err := os.Stat(path); err != nil {
		return c, err
	}
	tree, err := config.LoadYAMLTreeWithOptions(path, config.YAMLTreeOptions{RejectDuplicateKeys: true})
	if err != nil {
		return c, err
	}
	if values, ok := tree.(map[string]any); ok {
		if proxy, exists := values["httpProxy"]; exists {
			c.HTTPProxy, err = config.ParseHTTPProxy(proxy, c.HTTPProxy)
			if err != nil {
				return c, err
			}
			delete(values, "httpProxy")
		}
	}
	data, err := json.Marshal(tree)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	for _, p := range []*string{&c.ModelFile, &c.ProviderFile, &c.SsoIdTokenFile, &c.Auth.LocalPublicKeyFile} {
		*p = strings.TrimSpace(*p)
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	if c.Listen == "" || c.ModelFile == "" || c.ProviderFile == "" {
		return c, errors.New("listen, modelFile and providerFile are required")
	}
	if !c.Auth.Enabled || !c.Auth.RequireExpiry {
		return c, errors.New("dictation requires authentication and token expiry")
	}
	return c, c.HTTPProxy.Validate()
}

// Only these two files are read. A broken, unrelated registry entry cannot
// disable dictation, and credentials still use the existing provider resolver.
func (c Config) newASR() (*speech.ASRClient, error) {
	m, err := models.LoadModelFile(c.ModelFile)
	if err != nil {
		return nil, err
	}
	p, err := models.LoadProviderFile(c.ProviderFile, c.SsoIdTokenFile)
	if err != nil {
		return nil, err
	}
	if m.Key == "" || p.Key == "" || m.Provider != p.Key {
		return nil, errors.New("dictation model/provider mismatch")
	}
	return speech.NewASRClient(m, p, 600)
}
