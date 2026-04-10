package resources

import "embed"

//go:embed tools/*.yml
var ToolFS embed.FS
