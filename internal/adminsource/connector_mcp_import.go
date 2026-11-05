package adminsource

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

var connectorMCPHostSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// MCPImportOptions contains package configuration only, never credential values.
type MCPImportOptions struct {
	AuthMode    connector.AuthMode
	ID          string
	Description string
	IconPNG     string
	IconPreset  string
	Name        string
	TokenAuth   string
	HeaderName  string
}

// PrepareMCPImport generates an unpublished HTTP MCP package, using the same
// local validation as agent-proposed creation and the ordinary ZIP import path.
// Remote availability and authorization are checked separately after installation.
func PrepareMCPImport(ctx context.Context, sources connector.Sources, rawURL string, options MCPImportOptions) (*connector.PreparedArchive, error) {
	rawURL = strings.TrimSpace(rawURL)
	endpoint, err := url.Parse(rawURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || strings.Contains(rawURL, "#") {
		return nil, errors.New("mcpUrl must be an absolute HTTP(S) URL without userinfo or fragment")
	}
	name := strings.TrimSpace(options.Name)
	if name == "" || utf8.RuneCountInString(name) > 128 {
		return nil, errors.New("name is required and must be at most 128 characters")
	}
	id := strings.TrimSpace(options.ID)
	if id == "" {
		id = connectorMCPHostSeparators.ReplaceAllString(mcpImportURLIdentity(endpoint), "-")
		id = strings.Trim(id, "-")
		if len(id) > 64 {
			id = strings.TrimRight(id[:64], "-")
		}
	}
	// Directory identity is separate from the endpoint and remains stable after renaming.
	if !connector.ValidID(id) || len(id) > 64 || connector.IsBuiltin(id) {
		return nil, errors.New("id must be 1-64 lowercase ASCII letters, digits, dots, underscores or hyphens and cannot use builtin. prefix")
	}
	description := strings.TrimSpace(options.Description)
	if utf8.RuneCountInString(description) > 2000 {
		return nil, errors.New("description must be at most 2000 characters")
	}
	iconPath, iconData, err := mcpImportIcon(options)
	if err != nil {
		return nil, err
	}
	authMode := options.AuthMode
	manifest := connector.Manifest{ID: id, Name: name, Description: description, Icon: iconPath, Version: "1.0.0", Type: "mcp", AuthMode: authMode}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	mcpJSON, tokenSchema, err := mcpImportFiles(manifest, rawURL, endpoint, options)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries := []struct {
		name string
		data []byte
	}{{"connector.json", manifestJSON}, {"mcp.json", []byte(mcpJSON)}}
	if len(iconData) > 0 {
		entries = append(entries, struct {
			name string
			data []byte
		}{iconPath, iconData})
	}
	if len(tokenSchema) > 0 {
		entries = append(entries, struct {
			name string
			data []byte
		}{"token-schema.json", tokenSchema})
	}
	for _, entry := range entries {
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

var mcpImportHeader = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func mcpImportFiles(manifest connector.Manifest, rawURL string, endpoint *url.URL, options MCPImportOptions) (string, []byte, error) {
	if manifest.AuthMode != connector.AuthToken && (options.TokenAuth != "" || options.HeaderName != "") {
		return "", nil, errors.New("tokenAuth and headerName require authMode token")
	}
	component := map[string]any{"type": "streamableHttp", "url": rawURL}
	var schema []byte
	switch manifest.AuthMode {
	case connector.AuthMCP, connector.AuthNoAuth:
	case connector.AuthOneID:
		if endpoint.Scheme != "https" || endpoint.Port() != "" && endpoint.Port() != "443" {
			return "", nil, errors.New("oneid requires HTTPS on the default port")
		}
	case connector.AuthToken:
		kind := options.TokenAuth
		if kind == "" {
			kind = "bearer"
		}
		if kind != "header" && options.HeaderName != "" {
			return "", nil, errors.New("headerName requires tokenAuth header")
		}
		fields := []connector.TokenField{{Key: "TOKEN", Label: "Token", Type: "password", Required: true}}
		header, template := "Authorization", "Bearer ${TOKEN}"
		switch kind {
		case "bearer":
		case "header":
			header = strings.TrimSpace(options.HeaderName)
			lower := strings.ToLower(header)
			if !mcpImportHeader.MatchString(header) || strings.HasPrefix(lower, "mcp-") {
				return "", nil, errors.New("invalid API Key header name")
			}
			switch lower {
			case "host", "content-length", "connection", "transfer-encoding", "upgrade", "te", "trailer", "keep-alive", "proxy-authorization", "cookie", "accept", "content-type", "last-event-id":
				return "", nil, errors.New("reserved API Key header name")
			}
			template = "${TOKEN}"
			fields[0].Label = "API Key"
		case "basic":
			template = "Basic ${USERNAME}:${PASSWORD}"
			fields = []connector.TokenField{
				{Key: "USERNAME", Label: "Username", Type: "text", Required: true, I18N: map[string]connector.TokenFieldStrings{"zh-CN": {Label: "账号"}}},
				{Key: "PASSWORD", Label: "Password", Type: "password", Required: true, I18N: map[string]connector.TokenFieldStrings{"zh-CN": {Label: "密码"}}},
			}
		default:
			return "", nil, errors.New("tokenAuth must be bearer, header or basic")
		}
		component["headers"] = map[string]any{header: template}
		var err error
		schema, err = json.MarshalIndent(connector.TokenSchema{Fields: fields}, "", "  ")
		if err != nil {
			return "", nil, err
		}
		manifest.TokenSchema = schema
	default:
		return "", nil, errors.New("MCP URL import supports authMode mcp, none, oneid or token")
	}
	components := map[string]map[string]any{"main": component}
	if err := connectorauth.ValidatePackage(connector.Package{Manifest: manifest, MCP: components}); err != nil {
		return "", nil, err
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": components}, "", "  ")
	return string(data) + "\n", schema, err
}

// Only public endpoint identity seeds the ID; paths and query values may contain secrets.
func mcpImportURLIdentity(endpoint *url.URL) string {
	host := strings.ToLower(endpoint.Hostname())
	if host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		host = "local"
	}
	port := endpoint.Port()
	if endpoint.Scheme == "http" && port == "80" || endpoint.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host += "-" + port
	}
	return host
}
