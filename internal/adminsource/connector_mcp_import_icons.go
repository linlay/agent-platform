package adminsource

import (
	"encoding/base64"
	"errors"
)

// Original MCP artwork is shared with WebClient assets/mcp-icons. Uploaded icons use PNG.
const mcpImportMaxIconBytes = 32 << 10

var mcpImportIconPresets = map[string]string{
	"hub":      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#dcfaf2"/><stop offset="1" stop-color="#a6e7d4"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#147d68" stroke-linecap="round" stroke-linejoin="round"><path d="M17.7 10.8 15.2 8.3a4.3 4.3 0 0 0-6.1 6.1l2.5 2.5" stroke-width="3.4"/><path d="m14.3 21.2 2.5 2.5a4.3 4.3 0 0 0 6.1-6.1l-2.5-2.5" stroke-width="3.4"/><path d="m12.2 19.8 7.6-7.6" stroke="#fff" stroke-width="3.2"/><path d="m12.2 19.8 7.6-7.6" stroke-width="1.7"/></g></svg>`,
	"database": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#dff6ff"/><stop offset="1" stop-color="#abdef4"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#267baf" stroke-linecap="round" stroke-linejoin="round"><path d="m7 19 9 5 9-5-9-5z" fill="#73b6d9" stroke="none"/><path d="m7 14.5 9 5 9-5-9-5z" fill="#b3e2f5" stroke="#fff" stroke-width="1.2"/><path d="m7 10 9 5 9-5-9-5z" fill="#3189b5" stroke="none"/><path d="m16 6.7 5.8 3.3-5.8 3.3L10.2 10z" fill="#71c9e8" stroke="none"/><circle cx="24" cy="24" r="2" fill="#fff" stroke="none"/></g></svg>`,
	"terminal": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#e4ecff"/><stop offset="1" stop-color="#b8ccff"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#4166c5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 19.5a8 8 0 1 1 14.3-9.8" stroke="#7191da" stroke-width="1.5"/><path d="M24 12.5a8 8 0 0 1-14.3 9.8" stroke="#7191da" stroke-width="1.5"/><path d="m8.1 21.3-1.6-3.9 4.2.4M23.9 10.7l1.6 3.9-4.2-.4" stroke-width="1.5"/><rect x="10.5" y="10.5" width="11" height="11" rx="4" fill="#4166c5" stroke="none"/><path d="m15 13.5 3.5 2.5-3.5 2.5" stroke="#fff" stroke-width="1.6"/></g></svg>`,
	"code":     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#eee7ff"/><stop offset="1" stop-color="#d4c0fc"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#8551bd" stroke-linecap="round" stroke-linejoin="round"><path d="m16 5 9 8-3.5 11h-11L7 13z" fill="#ba91e7" stroke="none"/><path d="m16 5-3.5 11 9 8L25 13z" fill="#9866ce" stroke="none"/><path d="m7 13 5.5 3L16 5z" fill="#ead9ff" stroke="none"/><path d="m12.5 16-2 8h11z" fill="#ceaaf2" stroke="none"/><path d="m16 5-3.5 11 9 8M7 13l5.5 3L25 13" stroke="#fff" stroke-width="1.1" opacity=".8"/></g></svg>`,
	"folder":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#fff0d6"/><stop offset="1" stop-color="#f4d7a3"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#b87825" stroke-linecap="round" stroke-linejoin="round"><path d="M16 24C7 21 6.5 13 9 9c4 .5 7 4 7 8" fill="#dfad58" stroke="none"/><path d="M16 24c9-3 9.5-11 7-15-4 .5-7 4-7 8" fill="#bd8734" stroke="none"/><path d="M16 6c-5.5 5-5.5 10.5 0 15 5.5-4.5 5.5-10 0-15Z" fill="#fff6dc" stroke="none"/><path d="M16 12v13" stroke-width="1.4"/><path d="m10.5 15.5 5.5 6 5.5-6" stroke="#fff1cf" stroke-width="1.1"/></g></svg>`,
	"search":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#ffe9e2"/><stop offset="1" stop-color="#f6c4b5"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#c56a50" stroke-linecap="round" stroke-linejoin="round"><circle cx="16" cy="16" r="5.2" fill="#df8c70" stroke="none"/><ellipse cx="16" cy="16" rx="12" ry="5.4" transform="rotate(-35 16 16)" stroke-width="1.6"/><path d="M15 11.2a5 5 0 0 1 5 5" stroke="#ffdccf" stroke-width="1.5"/><circle cx="24.4" cy="10.1" r="2.4" fill="#fff9f2" stroke-width="1.1"/><circle cx="8" cy="24" r="1.2" fill="#c56a50" stroke="none"/></g></svg>`,
	"bolt":     `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#f4e6f8"/><stop offset="1" stop-color="#e1beeb"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#a25bb2" stroke-linecap="round" stroke-linejoin="round"><path d="m16 5 3.1 7.9L27 16l-7.9 3.1L16 27l-3.1-7.9L5 16l7.9-3.1Z" fill="#ae71c0" stroke="none"/><path d="m16 9 1.8 5.2L23 16l-5.2 1.8L16 23l-1.8-5.2L9 16l5.2-1.8Z" fill="#f9e9ff" stroke="none"/><circle cx="24.5" cy="7.5" r="1.6" fill="#a25bb2" stroke="none"/><circle cx="7.8" cy="24.2" r="1" fill="#a25bb2" stroke="none"/></g></svg>`,
	"build":    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none"><defs><linearGradient id="bg" x1="3" y1="0" x2="29" y2="32" gradientUnits="userSpaceOnUse"><stop stop-color="#e7f4df"/><stop offset="1" stop-color="#c6dfb1"/></linearGradient></defs><rect width="32" height="32" rx="9" fill="url(#bg)"/><rect x=".5" y=".5" width="31" height="31" rx="8.5" stroke="#fff" stroke-opacity=".45"/><g stroke="#618d3f" stroke-linecap="round" stroke-linejoin="round"><path d="M11 11h10v10H11z" stroke="#86aa6b" stroke-width="1.3"/><rect x="6" y="6" width="10" height="10" rx="3.2" fill="#6a984b" stroke="none"/><rect x="17" y="17" width="9" height="9" rx="3" fill="#93b77a" stroke="none"/><rect x="19.5" y="7" width="5" height="5" rx="1.8" fill="#f8ffef" stroke="none"/><rect x="7" y="20" width="5" height="5" rx="1.8" fill="#f8ffef" stroke="none"/><path d="m9.4 11 1.2 1.2 2.2-2.4" stroke="#f2ffe9" stroke-width="1.4"/></g></svg>`,
}

func mcpImportIcon(options MCPImportOptions) (string, []byte, error) {
	if options.IconPNG != "" && options.IconPreset != "" {
		return "", nil, errors.New("choose either iconPng or iconPreset")
	}
	if options.IconPreset != "" {
		svg, ok := mcpImportIconPresets[options.IconPreset]
		if !ok {
			return "", nil, errors.New("unknown iconPreset")
		}
		return "assets/icon.svg", []byte(svg), nil
	}
	if options.IconPNG == "" {
		return "", nil, nil
	}
	if len(options.IconPNG) > base64.StdEncoding.EncodedLen(mcpImportMaxIconBytes) {
		return "", nil, errors.New("PNG icon exceeds 32 KiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(options.IconPNG)
	if err != nil || len(data) == 0 || len(data) > mcpImportMaxIconBytes {
		return "", nil, errors.New("iconPng must be base64 PNG data of at most 32 KiB")
	}
	// PrepareArchive applies the existing full PNG decoder and dimensions limits.
	return "assets/icon.png", data, nil
}
