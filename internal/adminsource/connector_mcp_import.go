package adminsource

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"agent-platform/internal/connector"
)

var connectorMCPHostSeparators = regexp.MustCompile(`[^a-z0-9._-]+`)

// PrepareMCPImport generates an unpublished HTTP MCP package, using the same
// local validation as agent-proposed creation and the ordinary ZIP import path.
// Remote availability and authorization are checked separately after installation.
func PrepareMCPImport(ctx context.Context, sources connector.Sources, rawURL string, authMode connector.AuthMode) (*connector.PreparedArchive, error) {
	rawURL = strings.TrimSpace(rawURL)
	endpoint, err := url.Parse(rawURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || strings.Contains(rawURL, "#") {
		return nil, errors.New("mcpUrl must be an absolute HTTP(S) URL without userinfo or fragment")
	}
	// One host maps to one imported connector; paths and ports do not change its ID.
	id := strings.Trim(connectorMCPHostSeparators.ReplaceAllString(strings.ToLower(endpoint.Hostname()), "-"), "-.")
	if !connector.ValidID(id) || connector.IsBuiltin(id) {
		return nil, errors.New("mcpUrl host cannot be used as a connector id")
	}
	manifest := connector.Manifest{ID: id, Name: endpoint.Hostname(), Version: "1.0.0", Type: "mcp", AuthMode: authMode}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	mcpJSON, err := controlMCPFile(ControlChange{ControlTarget: ControlTarget{ResourceKey: id}, Content: string(manifestJSON), MCPURL: rawURL})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"connector.json", manifestJSON}, {"mcp.json", []byte(mcpJSON)}} {
		w, err := zw.Create(entry.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(entry.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return connector.PrepareArchive(ctx, sources, bytes.NewReader(buf.Bytes()), int64(buf.Len()))
}
