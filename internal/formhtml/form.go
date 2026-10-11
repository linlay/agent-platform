// Package formhtml owns the declarative form policy shared by validation and rendering.
package formhtml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const MaxBytes = 64 * 1024

// Policy is injected into the builtin page; keep all allowlists here.
var policy = struct {
	MaxBytes                                                    int `json:"maxBytes"`
	Tags, Attrs, SVGTags, SVGAttrs, Types, Functions, Positions []string
	Property, ExtraAttr, Forbidden, Paint                       string
}{
	MaxBytes:  MaxBytes,
	Tags:      strings.Fields("div span p h1 h2 h3 h4 h5 h6 label fieldset legend input textarea select option optgroup datalist ul ol li table thead tbody tr th td caption tfoot br hr strong em b i small code section header footer figure figcaption blockquote pre details summary dl dt dd mark u s sub sup progress meter"),
	Attrs:     strings.Fields("name id for class style type value placeholder required disabled readonly checked selected multiple min max step minlength maxlength pattern rows cols title colspan rowspan label list autocomplete inputmode width height open low high optimum role"),
	SVGTags:   strings.Fields("svg g path circle ellipse rect line polyline polygon text tspan title desc defs lineargradient stop"),
	SVGAttrs:  strings.Fields("id class style role width height xmlns viewbox preserveaspectratio x y x1 x2 y1 y2 cx cy r rx ry d points transform fill fill-opacity fill-rule stroke stroke-width stroke-opacity stroke-linecap stroke-linejoin stroke-dasharray stroke-dashoffset opacity color font-family font-size font-weight text-anchor dominant-baseline dx dy textlength lengthadjust gradientunits gradienttransform offset stop-color stop-opacity"),
	Types:     strings.Fields("text number email tel url date datetime-local time checkbox radio range color hidden"),
	Functions: strings.Fields("rgb rgba hsl hsla calc min max clamp repeat minmax var linear-gradient translate rotate scale"),
	Positions: strings.Fields("static relative absolute sticky initial inherit unset revert revert-layer"),
	Property:  `^[a-z-]+$`, ExtraAttr: `^(aria|data)-[a-z0-9_-]+$`,
	Forbidden: `[\\@{}<>\x00-\x08\x0b\x0c\x0e-\x1f]|/\*|\*/|expression`,
	Paint:     `^url\(#[a-zA-Z_][a-zA-Z0-9_.:-]*\)$`,
}

// PolicyJSON supplies the builtin page with the server policy as inert JSON.
func PolicyJSON() string {
	b, _ := json.Marshal(policy)
	return string(b)
}
func wordSet(words []string) map[string]bool {
	result := map[string]bool{}
	for _, word := range words {
		result[word] = true
	}
	return result
}

var formTags = wordSet(policy.Tags)
var formAttrs = wordSet(policy.Attrs)
var svgTags = wordSet(policy.SVGTags)
var svgAttrs = wordSet(policy.SVGAttrs)
var formInputTypes = wordSet(policy.Types)
var functions = wordSet(policy.Functions)
var positions = wordSet(policy.Positions)
var property = regexp.MustCompile(policy.Property)
var extraAttr = regexp.MustCompile(policy.ExtraAttr)
var forbidden = regexp.MustCompile("(?i)" + policy.Forbidden)
var paint = regexp.MustCompile(policy.Paint)
var identifier = regexp.MustCompile(`^[a-zA-Z_-][a-zA-Z0-9_-]*`)

// Control records the answer shape of a declared HTML form control.
type Control struct {
	kind     string
	count    int
	multiple bool
}

func safeValue(value string) bool {
	if strings.TrimSpace(value) == "" || forbidden.MatchString(value) {
		return false
	}
	depth := 0
	for i := 0; i < len(value); {
		c := value[i]
		if c == '\'' || c == '"' {
			quote := c
			i++
			for i < len(value) && value[i] != quote {
				i++
			}
			if i == len(value) {
				return false
			}
			i++
			continue
		}
		if match := identifier.FindStringIndex(value[i:]); match != nil && match[0] == 0 {
			end := i + match[1]
			name := strings.ToLower(value[i:end])
			i = end
			if i < len(value) && value[i] == '(' {
				if !functions[name] {
					return false
				}
				depth++
				i++
			}
			continue
		}
		if c == '(' {
			return false
		}
		if c == ')' {
			depth--
			if depth < 0 {
				return false
			}
		}
		i++
	}
	return depth == 0
}
func validateFormStyle(style string) error {
	for _, declaration := range strings.Split(style, ";") {
		if strings.TrimSpace(declaration) == "" {
			continue
		}
		key, value, ok := strings.Cut(declaration, ":")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		valid := ok && property.MatchString(key) && safeValue(value)
		if key == "position" {
			valid = valid && positions[strings.TrimSpace(strings.TrimSuffix(strings.ToLower(value), "!important"))]
		}
		if !valid {
			return fmt.Errorf("unsupported form style declaration %q; use CSS properties with safe values; allowed functions: %s; no url(), expression, escapes, comments, @ or position:fixed", declaration, strings.Join(policy.Functions, ", "))
		}
	}
	return nil
}
func validateAttrs(tag string, attrs []html.Attribute) error {
	allowed := formAttrs
	if svgTags[strings.ToLower(tag)] {
		allowed = svgAttrs
	}
	seen := map[string]bool{}
	for _, attr := range attrs {
		key := strings.ToLower(attr.Key)
		// The tree parser namespaces xmlns; no other namespaced attribute is allowed.
		if (attr.Namespace != "" && !(attr.Namespace == "xmlns" && key == "xmlns")) || (!allowed[key] && !extraAttr.MatchString(key)) || seen[key] {
			return fmt.Errorf("unsupported or duplicate form attribute %q", key)
		}
		seen[key] = true
		if key == "style" {
			if err := validateFormStyle(attr.Val); err != nil {
				return err
			}
		}
		if key == "xmlns" && attr.Val != "http://www.w3.org/2000/svg" {
			return fmt.Errorf("unsupported SVG namespace")
		}
		if tag == "input" && key == "type" && !formInputTypes[strings.ToLower(attr.Val)] {
			return fmt.Errorf("unsupported input type %q", attr.Val)
		}
		if svgTags[strings.ToLower(tag)] && (key == "fill" || key == "stroke") && !safeValue(attr.Val) && !paint.MatchString(attr.Val) {
			return fmt.Errorf("unsupported SVG paint %q; only colors or local url(#id) references", attr.Val)
		}
	}
	return nil
}

// Parse validates both tokens and the parsed namespace tree, then collects controls.
func Parse(fragment string) (map[string]Control, error) {
	if len(fragment) == 0 || len(fragment) > MaxBytes {
		return nil, fmt.Errorf("html must contain 1 to 65536 bytes")
	}
	// Inspect tokens too: the HTML parser silently drops some forbidden elements
	// (e.g. document wrappers), which must not turn invalid input into valid input.
	tokenizer := html.NewTokenizer(strings.NewReader(fragment))
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			if tokenizer.Err() != io.EOF {
				return nil, tokenizer.Err()
			}
			break
		}
		if tt == html.DoctypeToken {
			return nil, fmt.Errorf("html must be a fragment")
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken && tt != html.EndTagToken {
			continue
		}
		token := tokenizer.Token()
		if !formTags[token.Data] && !svgTags[token.Data] {
			return nil, fmt.Errorf("unsupported form tag %q", token.Data)
		}
		if err := validateAttrs(token.Data, token.Attr); err != nil {
			return nil, err
		}
	}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return nil, err
	}
	controls := map[string]Control{}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			if (n.Namespace != "" || !formTags[n.Data]) && (n.Namespace != "svg" || !svgTags[strings.ToLower(n.Data)]) {
				return fmt.Errorf("unsupported form element %q", n.Data)
			}
			if err := validateAttrs(n.Data, n.Attr); err != nil {
				return err
			}
			if n.Parent != nil && n.Parent.Namespace == "svg" && n.Namespace != "svg" {
				return fmt.Errorf("HTML controls and elements cannot be nested inside SVG")
			}
			if n.Data == "input" || n.Data == "select" || n.Data == "textarea" {
				attrs := map[string]string{}
				for _, a := range n.Attr {
					attrs[a.Key] = a.Val
				}
				name := attrs["name"]
				if strings.TrimSpace(name) == "" {
					return fmt.Errorf("every form control needs a name")
				}
				kind := n.Data
				if kind == "input" {
					kind = strings.ToLower(attrs["type"])
					if kind == "" {
						kind = "text"
					}
				}
				previous, exists := controls[name]
				if exists && (previous.kind != kind || (kind != "checkbox" && kind != "radio")) {
					return fmt.Errorf("duplicate control name %q", name)
				}
				_, multiple := attrs["multiple"]
				controls[name] = Control{kind: kind, count: previous.count + 1, multiple: kind == "select" && multiple}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range nodes {
		if err := walk(n); err != nil {
			return nil, err
		}
	}
	if len(controls) == 0 {
		return nil, fmt.Errorf("html needs at least one named control")
	}
	return controls, nil
}

// ValidateData checks the byte budget and answer types. Unknown submitted fields
// may be trimmed, but unknown defaults are rejected.
func ValidateData(data map[string]any, controls map[string]Control, trimUnknown bool) (map[string]any, error) {
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	err := enc.Encode(data)
	if err != nil || raw.Len()-1 > MaxBytes {
		return nil, fmt.Errorf("form values must be JSON of at most 65536 bytes")
	}
	result := map[string]any{}
	for key, value := range data {
		control, ok := controls[key]
		if !ok {
			if trimUnknown {
				continue
			}
			return nil, fmt.Errorf("undeclared form control %q", key)
		}
		array := control.multiple || (control.kind == "checkbox" && control.count > 1)
		if array {
			values := []string{}
			switch v := value.(type) {
			case []string:
				values = v
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf("%s must be a string array", key)
					}
					values = append(values, s)
				}
			default:
				return nil, fmt.Errorf("%s must be a string array", key)
			}
			result[key] = values
		} else {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string", key)
			}
			if control.kind == "checkbox" && s != "true" && s != "false" {
				return nil, fmt.Errorf("%s must be the string true or false", key)
			}
			result[key] = s
		}
	}
	return result, nil
}

// NormalizeValues tolerates JSON scalar defaults; submitted answers stay strict.
func NormalizeValues(value any, controls map[string]Control) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	data, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("values must be an object or null")
	}
	normalized := map[string]any{}
	scalar := func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return s, nil
		}
		switch v.(type) {
		case bool, float64, float32, int, int64, int32, uint, uint64, json.Number:
			b, err := json.Marshal(v)
			if err == nil {
				return string(b), nil
			}
		}
		return "", fmt.Errorf("initial values must be strings, numbers or booleans")
	}
	for key, value := range data {
		switch v := value.(type) {
		case []string:
			normalized[key] = v
		case []any:
			items := []string{}
			for _, item := range v {
				s, err := scalar(item)
				if err != nil {
					return nil, err
				}
				items = append(items, s)
			}
			normalized[key] = items
		default:
			s, err := scalar(v)
			if err != nil {
				return nil, err
			}
			normalized[key] = s
		}
	}
	return ValidateData(normalized, controls, false)
}
