// Package connectorops exposes explicitly declared connector operations without
// entering the Agent tool loop. Package scripts and discovered MCP tools are not
// automatically application capabilities.
package connectorops

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agent-platform/internal/connector"
	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed profiles/*.json
var profiles embed.FS

const MaxJSONBytes = 1 << 20

type Operation struct {
	ID            string          `json:"operationId"`
	Description   string          `json:"description"`
	Effect        string          `json:"effect"`
	InputSchema   json.RawMessage `json:"inputSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema"`
	Adapter       string          `json:"-"`
	CLI           *CLI            `json:"-"`
	MCP           *MCP            `json:"-"`
	input, output *jsonschema.Resolved
}
type CLI struct {
	Entry        string         `json:"entry"`
	Args         []string       `json:"args"`
	JSONFlag     string         `json:"jsonFlag,omitempty"`
	EntryWindows string         `json:"entryWindows,omitempty"`
	Parameters   []CLIParameter `json:"parameters,omitempty"`
	DropFields   []string       `json:"dropFields,omitempty"`
	TextFields   []TextField    `json:"textFields,omitempty"`
}
type MCP struct {
	Component string `json:"component"`
	Tool      string `json:"tool"`
}
type Catalog struct {
	ConnectorID string      `json:"connectorId"`
	Revision    string      `json:"revision"`
	Operations  []Operation `json:"operations"`
}
type declaration struct {
	ID           string          `json:"operationId"`
	Description  string          `json:"description"`
	Effect       string          `json:"effect"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Adapter      string          `json:"adapter"`
	CLI          *CLI            `json:"cli,omitempty"`
	MCP          *MCP            `json:"mcp,omitempty"`
}

// Load reads a bounded, package-owned operations.json, never CLI --help or a
// Skill document. The revision includes the complete package fingerprint.
func Load(pkg connector.Package) (Catalog, error) {
	result := Catalog{ConnectorID: pkg.ID, Operations: []Operation{}}
	p := filepath.Join(pkg.Dir, "operations.json")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		// Platform profiles provide reviewed mappings without changing installed
		// connector packages. A package manifest takes precedence, never merges.
		data, e := profiles.ReadFile("profiles/" + pkg.ID + ".json")
		if e != nil {
			return result, nil
		}
		return loadData(pkg, data)
	}
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxJSONBytes {
		return result, fmt.Errorf("invalid operation manifest")
	}
	f, err := os.Open(p)
	if err != nil {
		return result, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxJSONBytes+1))
	if err != nil || len(data) > MaxJSONBytes {
		return result, fmt.Errorf("invalid operation manifest")
	}
	return loadData(pkg, data)
}

func loadData(pkg connector.Package, data []byte) (Catalog, error) {
	result := Catalog{ConnectorID: pkg.ID, Operations: []Operation{}}
	var err error
	var raw struct {
		Version    int           `json:"version"`
		Operations []declaration `json:"operations"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return result, err
	}
	if d.Decode(new(any)) != io.EOF || raw.Version != 1 || len(raw.Operations) > 128 {
		return result, fmt.Errorf("invalid operation manifest")
	}
	seen := map[string]bool{}
	for _, item := range raw.Operations {
		if !connector.ValidID(item.ID) || len(item.ID) > 128 || seen[item.ID] || (item.Effect != "read" && item.Effect != "write") {
			return result, fmt.Errorf("invalid or unsupported operation")
		}
		seen[item.ID] = true
		op := Operation{ID: item.ID, Description: item.Description, Effect: item.Effect, InputSchema: item.InputSchema, OutputSchema: item.OutputSchema, Adapter: item.Adapter, CLI: item.CLI, MCP: item.MCP}
		op.input, err = resolveSchema(op.InputSchema)
		if err != nil {
			return result, err
		}
		op.output, err = resolveSchema(op.OutputSchema)
		if err != nil {
			return result, err
		}
		switch op.Adapter {
		case "cli":
			if op.CLI == nil || op.MCP != nil || pkg.CLI == nil || len(op.CLI.Args) > 16 {
				return result, fmt.Errorf("invalid CLI operation")
			}
			if err = validateCLI(*op.CLI); err != nil {
				return result, err
			}
			if _, err = Entry(pkg, cliEntryForOS(*op.CLI, runtime.GOOS)); err != nil {
				return result, err
			}
			for _, arg := range op.CLI.Args {
				if arg == "" || strings.ContainsAny(arg, "\x00\r\n") {
					return result, fmt.Errorf("invalid CLI argument")
				}
			}
		case "mcp":
			if op.MCP == nil || op.CLI != nil || pkg.MCP[op.MCP.Component] == nil || op.MCP.Tool == "" || len(op.MCP.Tool) > 256 {
				return result, fmt.Errorf("invalid MCP operation")
			}
		default:
			return result, fmt.Errorf("invalid operation adapter")
		}
		result.Operations = append(result.Operations, op)
	}
	fingerprint, err := connector.RuntimeFingerprint(pkg.Dir)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(append([]byte(fingerprint), data...))
	result.Revision = hex.EncodeToString(digest[:])
	return result, nil
}
func resolveSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if len(raw) == 0 || json.Unmarshal(raw, &schema) != nil || schema.Type != "object" {
		return nil, fmt.Errorf("operation schema must describe an object")
	}
	return schema.Resolve(nil) // No remote schema loader: references cannot fetch URLs.
}

// Entry allows a package-owned native executable only. Shell launchers and
// Windows command scripts cannot safely transport arbitrary JSON argv.
func Entry(pkg connector.Package, relative string) (string, error) {
	if !strings.HasPrefix(relative, "bin/") || strings.ContainsAny(relative, "\\\x00") || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid operation entry")
	}
	clean := filepath.ToSlash(filepath.Clean(relative))
	if clean != relative {
		return "", fmt.Errorf("invalid operation entry")
	}
	root, err := filepath.EvalSymlinks(pkg.Dir)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("operation entry escapes package")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid operation executable")
	}
	var magic [4]byte
	if _, err = io.ReadFull(f, magic[:]); err != nil {
		return "", fmt.Errorf("invalid operation executable")
	}
	// ELF, PE and Mach-O (including universal binaries); never execute a shebang.
	valid := string(magic[:]) == "\x7fELF" || string(magic[:2]) == "MZ" || string(magic[:]) == "\xcf\xfa\xed\xfe" || string(magic[:]) == "\xfe\xed\xfa\xcf" || string(magic[:]) == "\xca\xfe\xba\xbe" || string(magic[:]) == "\xbe\xba\xfe\xca"
	if !valid {
		return "", fmt.Errorf("operation entry must be a native executable")
	}
	return path, nil
}
