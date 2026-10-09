package toolinteraction

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const maxFormBytes = 64 * 1024

var formTags = wordSet("div span p h1 h2 h3 h4 label fieldset legend input textarea select option optgroup ul ol li table thead tbody tr th td br hr strong em b i small code")
var formAttrs = wordSet("name id for class style type value placeholder required disabled readonly checked selected multiple min max step minlength maxlength pattern rows cols title colspan rowspan label")
var formInputTypes = wordSet("text number email tel url date datetime-local time checkbox radio range color hidden")
var formStyleProperties = wordSet("display flex flex-direction flex-wrap align-items justify-content gap row-gap column-gap grid-template-columns width min-width max-width margin margin-top margin-right margin-bottom margin-left padding padding-top padding-right padding-bottom padding-left color background-color font-size font-weight line-height text-align border border-width border-style border-color border-radius")
var formStyleValue = regexp.MustCompile(`^[a-zA-Z0-9#%., +\-]+$`)
var formAriaAttr = regexp.MustCompile(`^aria-[a-z-]+$`)

type formControl struct {
	kind     string
	count    int
	multiple bool
}

func wordSet(words string) map[string]bool {
	result := map[string]bool{}
	for _, word := range strings.Fields(words) {
		result[word] = true
	}
	return result
}

// Reject CSS escapes, comments, functions and positioning rather than trying to
// blacklist executable or network-bearing spellings of arbitrary CSS.
func validateFormStyle(style string) error {
	for _, declaration := range strings.Split(style, ";") {
		if strings.TrimSpace(declaration) == "" {
			continue
		}
		key, value, ok := strings.Cut(declaration, ":")
		if !ok || !formStyleProperties[strings.ToLower(strings.TrimSpace(key))] || !formStyleValue.MatchString(strings.TrimSpace(value)) {
			return fmt.Errorf("unsupported form style declaration %q", declaration)
		}
	}
	return nil
}

func parseFormHTML(fragment string) (map[string]formControl, error) {
	if len(fragment) == 0 || len(fragment) > maxFormBytes {
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
		if !formTags[token.Data] {
			return nil, fmt.Errorf("unsupported form tag %q", token.Data)
		}
		seen := map[string]bool{}
		for _, attr := range token.Attr {
			if attr.Namespace != "" || (!formAttrs[attr.Key] && !formAriaAttr.MatchString(attr.Key)) || seen[attr.Key] {
				return nil, fmt.Errorf("unsupported or duplicate form attribute %q", attr.Key)
			}
			seen[attr.Key] = true
			if attr.Key == "style" {
				if err := validateFormStyle(attr.Val); err != nil {
					return nil, err
				}
			}
			if token.Data == "input" && attr.Key == "type" && !formInputTypes[strings.ToLower(attr.Val)] {
				return nil, fmt.Errorf("unsupported input type %q", attr.Val)
			}
		}
	}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return nil, err
	}
	controls := map[string]formControl{}
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if n.Type == html.ElementNode {
			if n.Namespace != "" || !formTags[n.Data] {
				return fmt.Errorf("unsupported form element %q", n.Data)
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
				controls[name] = formControl{kind: kind, count: previous.count + 1, multiple: kind == "select" && multiple}
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

func validateFormData(data map[string]any, controls map[string]formControl, trimUnknown bool) (map[string]any, error) {
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > maxFormBytes {
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
