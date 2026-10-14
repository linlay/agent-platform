package connectorauth

import (
	"strings"
	"testing"

	"agent-platform/internal/hostenv"
)

func TestCLIBaseEnvironmentKeepsNetworkConfigurationWithoutAmbientCredentials(t *testing.T) {
	source := []string{
		"PATH=", "PATHEXT=.EXE;.CMD", "HTTPS_PROXY=http://proxy.example.test:8080",
		"http_proxy=http://proxy.example.test:8081", "NO_PROXY=jira.example.test,localhost",
		"SSL_CERT_FILE=/fixture/ca.pem", "SSL_CERT_DIR=/fixture/certs", "SystemRoot=C:\\Windows",
		"NODE_EXTRA_CA_CERTS=/fixture/company-ca.pem", "USER=fixture-user", "USERNAME=fixture-user",
		"NPM_CONFIG_REGISTRY=https://registry.example.test/", "npm_config_cafile=/fixture/npm-ca.pem",
		"NPM_CONFIG_PROXY=http://proxy.example.test:8080", "NPM_CONFIG_HTTPS_PROXY=http://proxy.example.test:8080",
		"NPM_CONFIG_NOPROXY=registry.internal", "NPM_TOKEN=must-not-inherit", "NPM_CONFIG__AUTH=must-not-inherit",
		"NPM_CONFIG_USERCONFIG=/ambient/.npmrc", "NPM_CONFIG_PREFIX=/ambient/global", "NPM_CONFIG_CACHE=/ambient/cache",
		"AP_ACCESS_TOKEN=must-not-inherit", "SSH_AUTH_SOCK=/fixture/ssh-agent",
		"JIRA_PASSWORD=must-not-inherit", "OTHER_CONNECTOR_SECRET=must-not-inherit",
		"HOME=/ambient/home", "TMPDIR=/ambient/tmp", "LD_PRELOAD=/fixture/untrusted.so",
	}
	env := cliBaseEnvironmentFrom(source)
	for _, key := range []string{"PATHEXT", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "SystemRoot", "NODE_EXTRA_CA_CERTS", "USER", "USERNAME", "NPM_CONFIG_REGISTRY", "npm_config_cafile", "NPM_CONFIG_PROXY", "NPM_CONFIG_HTTPS_PROXY", "NPM_CONFIG_NOPROXY"} {
		if hostenv.Value(env, key) != hostenv.Value(source, key) {
			t.Fatalf("lost required host setting %s", key)
		}
	}
	if !strings.Contains(strings.Join(env, "\n"), "http_proxy=http://proxy.example.test:8081") {
		t.Fatal("lower-case proxy setting was lost")
	}
	for _, key := range []string{"AP_ACCESS_TOKEN", "SSH_AUTH_SOCK", "JIRA_PASSWORD", "OTHER_CONNECTOR_SECRET", "HOME", "TMPDIR", "LD_PRELOAD", "NPM_TOKEN", "NPM_CONFIG__AUTH", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_PREFIX", "NPM_CONFIG_CACHE"} {
		if hostenv.Value(env, key) != "" {
			t.Fatalf("unsafe ambient variable inherited: %s", key)
		}
	}
}
