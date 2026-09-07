package resources

import "embed"

//go:embed builtin_tool_catalog.yml tools/*.yml
var ToolFS embed.FS

// ConnectorFS contains the complete, versioned builtin connector resources.
// Native executables are supplied by the verified builtin build cache.
//
//go:embed all:connectors
var ConnectorFS embed.FS
