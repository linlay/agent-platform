package connectorauth

import (
	"os"
	"strings"

	"agent-platform/internal/builtins"
	"agent-platform/internal/connector"
	"agent-platform/internal/hostenv"
)

// CLIBaseEnvironment is the same controlled host environment for lifecycle and
// business calls. Network/proxy/CA settings survive, but deployment identity,
// SSH agent credentials and unrelated connector variables never inherit.
// Installation paths, private HOME/tmp and this connector's declared settings
// and credentials are applied separately for the particular execution phase.
func CLIBaseEnvironment(pkg connector.Package) ([]string, error) {
	env := cliBaseEnvironmentFrom(os.Environ())
	values, err := pkg.CLIConfigEnvironment()
	if err != nil {
		return nil, err
	}
	for key, value := range values {
		env = hostenv.Set(env, key, value)
	}
	return env, nil
}

func cliBaseEnvironmentFrom(source []string) []string {
	var env []string
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(key) {
		case "PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "USER", "LOGNAME", "USERNAME", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TERM", "NO_COLOR",
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
			"NPM_CONFIG_REGISTRY", "NPM_CONFIG_CAFILE", "NPM_CONFIG_PROXY", "NPM_CONFIG_HTTPS_PROXY", "NPM_CONFIG_NOPROXY":
			env = append(env, entry)
		}
	}
	return builtins.EnsureBinInEnv(hostenv.WithSystemPaths(env))
}
