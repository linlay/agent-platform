package connector

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"agent-platform/internal/view"
)

func (p Package) ViewMount() view.Mount {
	return view.Mount{ID: p.ID, Version: p.Version, Dir: p.Dir, Views: p.Views}
}

var viewCredentialPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolveViewHeaders uses only deployment credentials, never process env.
func ResolveViewHeaders(stateRoot, id string, headers map[string]string) (map[string]string, error) {
	credentials := map[string]string{}
	needsCredentials := false
	for _, value := range headers {
		needsCredentials = needsCredentials || strings.Contains(value, "${")
	}
	if needsCredentials {
		path, err := CredentialsPath(stateRoot, id)
		if err != nil {
			return nil, err
		}
		if err := ReadJSON(path, &credentials); err != nil {
			return nil, fmt.Errorf("view credentials unavailable")
		}
	}
	resolved := make(map[string]string, len(headers))
	for key, value := range headers {
		missing := false
		value = viewCredentialPattern.ReplaceAllStringFunc(value, func(token string) string {
			v, ok := credentials[token[2:len(token)-1]]
			if !ok || v == "" {
				missing = true
			}
			return v
		})
		if missing || strings.Contains(value, "${") || strings.ContainsAny(value, "\r\n\x00") {
			return nil, os.ErrPermission
		}
		resolved[key] = value
	}
	return resolved, nil
}
