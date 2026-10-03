package resources

import "embed"

//go:embed builtin_tool_catalog.yml tools/*.yml
var ToolFS embed.FS

// ConnectorFS contains only the Platform-owned native Desktop connector variants.
// dbx/httpx complete packages are supplied by the verified build cache.
//
//go:embed all:connectors
var ConnectorFS embed.FS

// ViewportFS contains Platform-owned HTML forms, served by fixed builtin keys.
//
//go:embed viewports/*.html
var ViewportFS embed.FS
