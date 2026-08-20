package conversationexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const (
	TemplateMarker    = "__CONVERSATION_EXPORT_SNAPSHOT_JSON_V1__"
	AssetOriginMarker = "__CONVERSATION_EXPORT_ASSET_ORIGIN__"
	TemplateProfile   = `<meta name="conversation-export-profile" content="conversation-snapshot-json-v1"`
)

var ErrTemplateInvalid = errors.New("conversation export template is invalid")
var ErrAssetOriginInvalid = errors.New("conversation export asset origin is invalid")
var templateAssetSetPattern = regexp.MustCompile(`<meta\s+name="conversation-export-asset-set"\s+content="([a-f0-9]{64})"`)

var templateMarkerBytes = []byte(TemplateMarker)
var assetOriginMarkerBytes = []byte(AssetOriginMarker)

type HTMLRenderer struct {
	template []byte
}

func NewHTMLRenderer(template []byte) (*HTMLRenderer, error) {
	if len(template) == 0 || len(template) > MaxTemplateBytes {
		return nil, ErrTemplateInvalid
	}
	marker := []byte(TemplateMarker)
	markerOffset := bytes.Index(template, marker)
	if markerOffset < 0 || markerOffset != bytes.LastIndex(template, marker) || bytes.Count(template, []byte(TemplateProfile)) != 1 || !validExternalAssetTemplate(template, marker) {
		return nil, ErrTemplateInvalid
	}
	return &HTMLRenderer{template: bytes.Clone(template)}, nil
}

func validExternalAssetTemplate(template, marker []byte) bool {
	lower := bytes.ToLower(template)
	assetSetMatches := templateAssetSetPattern.FindAllSubmatch(template, -1)
	if bytes.Contains(lower, []byte("<style")) ||
		bytes.Contains(lower, []byte("cdn.jsdelivr.net")) ||
		len(assetSetMatches) != 1 ||
		bytes.Count(template, []byte(AssetOriginMarker)) != 5 {
		return false
	}
	assetSet := assetSetMatches[0][1]
	assetOriginMarker := bytes.ToLower([]byte(AssetOriginMarker))
	assetPath := append(append([]byte("/assets/conversation-export/"), assetSet...), '/')
	cssAssetURL := append(append(bytes.Clone(assetOriginMarker), assetPath...), []byte("runtime.css")...)
	jsAssetURL := append(append(bytes.Clone(assetOriginMarker), assetPath...), []byte("runtime.js")...)
	for _, directive := range [][]byte{
		append([]byte("font-src "), assetOriginMarker...),
		append([]byte("style-src-elem "), assetOriginMarker...),
		append([]byte("script-src "), assetOriginMarker...),
	} {
		if !bytes.Contains(lower, directive) {
			return false
		}
	}

	stylesheets := 0
	remainingLinks := template
	for {
		lowerRemaining := bytes.ToLower(remainingLinks)
		start := bytes.Index(lowerRemaining, []byte("<link"))
		if start < 0 {
			break
		}
		tagEndOffset := bytes.Index(lowerRemaining[start:], []byte(">"))
		if tagEndOffset < 0 {
			return false
		}
		tagEnd := start + tagEndOffset
		attributes := lowerRemaining[start : tagEnd+1]
		if !bytes.Contains(attributes, []byte(`rel="stylesheet"`)) ||
			!bytes.Contains(attributes, []byte("href=")) ||
			!bytes.Contains(attributes, cssAssetURL) ||
			!bytes.Contains(attributes, []byte("integrity=")) ||
			!bytes.Contains(attributes, []byte("crossorigin=")) {
			return false
		}
		stylesheets++
		remainingLinks = remainingLinks[tagEnd+1:]
	}

	inlineDataScripts := 0
	externalScripts := 0
	remaining := template
	for {
		lowerRemaining := bytes.ToLower(remaining)
		start := bytes.Index(lowerRemaining, []byte("<script"))
		if start < 0 {
			break
		}
		tagEndOffset := bytes.Index(lowerRemaining[start:], []byte(">"))
		if tagEndOffset < 0 {
			return false
		}
		tagEnd := start + tagEndOffset
		closeOffset := bytes.Index(lowerRemaining[tagEnd+1:], []byte("</script>"))
		if closeOffset < 0 {
			return false
		}
		closeStart := tagEnd + 1 + closeOffset
		attributes := lowerRemaining[start : tagEnd+1]
		body := bytes.TrimSpace(remaining[tagEnd+1 : closeStart])
		switch {
		case bytes.Contains(attributes, []byte("src=")):
			if len(body) != 0 ||
				!bytes.Contains(attributes, jsAssetURL) ||
				!bytes.Contains(attributes, []byte("integrity=")) ||
				!bytes.Contains(attributes, []byte("crossorigin=")) {
				return false
			}
			externalScripts++
		case bytes.Contains(attributes, []byte(`type="application/json"`)):
			if !bytes.Equal(body, marker) {
				return false
			}
			inlineDataScripts++
		default:
			return false
		}
		remaining = remaining[closeStart+len("</script>"):]
	}
	return stylesheets == 1 && inlineDataScripts == 1 && externalScripts == 1
}

func (r *HTMLRenderer) Render(snapshot SnapshotV1, assetOrigin string) ([]byte, error) {
	if r == nil {
		return nil, ErrTemplateInvalid
	}
	normalizedAssetOrigin, err := normalizeAssetOrigin(assetOrigin)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxSnapshotBytes {
		return nil, newSizeLimitError(len(encoded), MaxSnapshotBytes)
	}
	assetOriginReferences := bytes.Count(r.template, assetOriginMarkerBytes)
	finalSize := len(r.template) - len(TemplateMarker) + len(encoded) -
		assetOriginReferences*len(AssetOriginMarker) +
		assetOriginReferences*len(normalizedAssetOrigin)
	if finalSize > MaxHTMLBytes {
		return nil, newSizeLimitError(finalSize, MaxHTMLBytes)
	}
	return renderTemplate(
		r.template,
		encoded,
		[]byte(normalizedAssetOrigin),
		finalSize,
	), nil
}

func renderTemplate(template, snapshot, assetOrigin []byte, finalSize int) []byte {
	result := make([]byte, 0, finalSize)
	remaining := template
	for len(remaining) > 0 {
		snapshotOffset := bytes.Index(remaining, templateMarkerBytes)
		assetOriginOffset := bytes.Index(remaining, assetOriginMarkerBytes)
		switch {
		case snapshotOffset >= 0 && (assetOriginOffset < 0 || snapshotOffset < assetOriginOffset):
			result = append(result, remaining[:snapshotOffset]...)
			result = append(result, snapshot...)
			remaining = remaining[snapshotOffset+len(templateMarkerBytes):]
		case assetOriginOffset >= 0:
			result = append(result, remaining[:assetOriginOffset]...)
			result = append(result, assetOrigin...)
			remaining = remaining[assetOriginOffset+len(assetOriginMarkerBytes):]
		default:
			result = append(result, remaining...)
			remaining = nil
		}
	}
	return result
}

func normalizeAssetOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme == "" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.ForceQuery ||
		(parsed.Path != "" && parsed.Path != "/") ||
		strings.ContainsAny(value, "\r\n\t\"'<>") {
		return "", ErrAssetOriginInvalid
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", ErrAssetOriginInvalid
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isLoopbackHost(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
