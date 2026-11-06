package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/agentengine"
)

func (s *Server) handleLocalAgentPlugins(w http.ResponseWriter, r *http.Request) {
	engines := s.deps.AgentEngines
	action := strings.TrimPrefix(r.URL.Path, "/api/local/agent-plugins")
	if action == "" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": engines.Directory()})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if action == "/import" {
		file, err := os.CreateTemp("", "agent-engine-upload-")
		if err != nil {
			engineError(w, err)
			return
		}
		defer os.Remove(file.Name())
		defer file.Close()
		size, err := io.Copy(file, http.MaxBytesReader(w, r.Body, agentengine.MaxArchiveBytes))
		if err != nil {
			engineError(w, agentengine.Error("invalid_arguments"))
			return
		}
		overwrite := r.URL.Query().Get("overwrite")
		if overwrite != "" && overwrite != "true" && overwrite != "false" {
			engineError(w, agentengine.Error("invalid_arguments"))
			return
		}
		ack, err := engines.InstallArchive(file, size, overwrite == "true")
		if err != nil {
			engineError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": ack})
		return
	}
	if action != "/enable" && action != "/disable" && action != "/uninstall" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var request struct {
		PluginID string `json:"pluginId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.PluginID == "" || decoder.Decode(new(any)) != io.EOF {
		engineError(w, agentengine.Error("invalid_arguments"))
		return
	}
	ack, err := engines.ChangePlugin(request.PluginID, strings.TrimPrefix(action, "/"))
	if err != nil {
		engineError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"code": 200, "msg": "ok", "data": ack})
}
