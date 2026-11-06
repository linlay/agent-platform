package agentengine

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"agent-platform/internal/runtimefs"
)

type LocalManagement struct {
	URL    string `json:"url"`
	HostID string `json:"hostId"`
	Epoch  string `json:"epoch"`
	Secret string `json:"secret"`
}

func (s *Service) IsStandalone() bool { return s.standalone }

func (s *Service) PublishLocalManagement(port string) error {
	if !s.standalone {
		return Error("unsupported_deployment")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Error("invalid_arguments")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.closed {
		return Error("registry_not_ready")
	}
	access := LocalManagement{URL: "http://" + net.JoinHostPort("127.0.0.1", port) + "/api/local/agent-plugins", HostID: s.authority.HostID, Epoch: s.epoch, Secret: s.authority.ManagementSecret}
	return writePrivateJSON(filepath.Join(s.root, "agent-engine-installations", "management.json"), access)
}

// Only the OS administrator of this deployment can obtain the process secret.
// It is never returned through the ordinary browser API or passed in argv.
func ReadLocalManagement(root string) (LocalManagement, error) {
	var result LocalManagement
	dir := filepath.Join(root, "agent-engine-installations")
	file := filepath.Join(dir, "management.json")
	for _, p := range []string{dir, file} {
		if err := runtimefs.PrivateOwned(p); err != nil {
			return result, err
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return LocalManagement{}, Error("artifact_untrusted")
	}
	parsed, err := url.Parse(result.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/api/local/agent-plugins" || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.Port() == "" || len(result.Secret) != 64 || result.HostID == "" || result.Epoch == "" {
		return LocalManagement{}, Error("artifact_untrusted")
	}
	return result, nil
}
