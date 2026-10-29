package formhtml

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Field describes a validated control to a caller that cannot render the HTML.
type Field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Multiple bool     `json:"multiple,omitempty"`
	Options  []string `json:"options,omitempty"`
}

func Describe(fragment string) ([]Field, error) {
	controls, err := Parse(fragment)
	if err != nil {
		return nil, err
	}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return nil, err
	}
	fields := []Field{}
	indices := map[string]int{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		attrs := map[string]string{}
		for _, a := range n.Attr {
			attrs[a.Key] = a.Val
		}
		if c, ok := controls[attrs["name"]]; ok && (n.Data == "input" || n.Data == "select" || n.Data == "textarea") {
			index, exists := indices[attrs["name"]]
			if !exists {
				index = len(fields)
				indices[attrs["name"]] = index
				fields = append(fields, Field{Name: attrs["name"], Type: c.kind, Multiple: c.multiple || c.kind == "checkbox" && c.count > 1})
			}
			if c.kind == "checkbox" || c.kind == "radio" {
				fields[index].Options = append(fields[index].Options, attrs["value"])
			}
			if c.kind == "select" {
				var options func(*html.Node)
				options = func(node *html.Node) {
					if node.Data == "option" {
						value := ""
						found := false
						for _, a := range node.Attr {
							if a.Key == "value" {
								value = a.Val
								found = true
							}
						}
						if !found {
							var text func(*html.Node)
							text = func(t *html.Node) {
								if t.Type == html.TextNode {
									value += t.Data
								}
								for child := t.FirstChild; child != nil; child = child.NextSibling {
									text(child)
								}
							}
							text(node)
						}
						fields[index].Options = append(fields[index].Options, value)
					}
					for child := node.FirstChild; child != nil; child = child.NextSibling {
						options(child)
					}
				}
				options(n)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return fields, nil
}
