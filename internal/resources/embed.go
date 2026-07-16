package resources

import "embed"

//go:embed builtin_tool_catalog.yml tools/*.yml
var ToolFS embed.FS
