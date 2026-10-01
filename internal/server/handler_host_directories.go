package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/api"
)

const hostDirectoryListLimit = 2000

// handleHostDirectories lets a client that is not running on the Platform
// host pick a project directory there. It uses the same authentication as the
// agent management endpoints, which can already point an agent at any host
// directory, and it only ever returns directory names.
func (s *Server) handleHostDirectories(w http.ResponseWriter, r *http.Request) {
	showHidden := false
	switch strings.TrimSpace(r.URL.Query().Get("showHidden")) {
	case "", "false":
	case "true":
		showHidden = true
	default:
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "showHidden must be true or false"))
		return
	}
	response, err := listHostDirectories(r.URL.Query().Get("path"), showHidden)
	s.writeAgentHTTPResponse(w, response, err)
}

func listHostDirectories(requested string, showHidden bool) (api.HostDirectoryListResponse, error) {
	home, _ := os.UserHomeDir()
	target := strings.TrimSpace(requested)
	if target == "" {
		target = home
	}
	if target == "" || !filepath.IsAbs(target) {
		return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path must be an absolute directory on the Platform host")
	}
	target = filepath.Clean(target)
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "directory does not exist on the Platform host")
		}
		if os.IsPermission(err) {
			return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusForbidden, "forbidden", "the Platform cannot access this directory")
		}
		return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if !info.IsDir() {
		return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path is not a directory")
	}
	items, err := os.ReadDir(target)
	if err != nil {
		if os.IsPermission(err) {
			return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusForbidden, "forbidden", "the Platform cannot list this directory")
		}
		return api.HostDirectoryListResponse{}, newAgentStatusError(http.StatusInternalServerError, "list_failed", err.Error())
	}
	entries := make([]api.HostDirectoryEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(target, name)
		isDir := item.IsDir()
		if !isDir && item.Type()&os.ModeSymlink != 0 {
			// Follow a link only to learn whether it leads to a directory.
			if linked, statErr := os.Stat(full); statErr == nil {
				isDir = linked.IsDir()
			}
		}
		if isDir {
			entries = append(entries, api.HostDirectoryEntry{Name: name, Path: full})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if left != right {
			return left < right
		}
		return entries[i].Name < entries[j].Name
	})
	response := api.HostDirectoryListResponse{
		Path:      target,
		Home:      home,
		Separator: string(filepath.Separator),
		Entries:   entries,
	}
	if len(entries) > hostDirectoryListLimit {
		response.Entries = entries[:hostDirectoryListLimit]
		response.Truncated = true
	}
	if parent := filepath.Dir(target); parent != target {
		response.Parent = parent
	}
	return response, nil
}
