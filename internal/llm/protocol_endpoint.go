package llm

import (
	"net/url"
	"strings"
)

func defaultEndpointPath(protocol string, baseURL string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "OPENAI_RESPONSES":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/responses"
		}
		return "/v1/responses"
	case "ANTHROPIC":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/messages"
		}
		return "/v1/messages"
	case "", "OPENAI":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	default:
		return ""
	}
}

func normalizedBasePath(rawBaseURL string) string {
	parsed, err := urlParse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(parsed.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(parsed.Path)
	}
	if path == "" || path == "/" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimSpace(path), "/")
}

func urlParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
