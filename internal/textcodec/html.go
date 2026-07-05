package textcodec

import (
	"strings"
	"unicode"

	"agent-platform/internal/runtimeenv"

	"golang.org/x/net/html"
)

// HTMLToMarkdownLike extracts visible HTML text into a compact Markdown-like form.
func HTMLToMarkdownLike(data []byte) string {
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	if decoded, ok, err := DecodeFileText(data, "", runtimeenv.Detect()); err == nil && ok {
		text = decoded.Content
	}
	return HTMLTextToMarkdownLike(text)
}

func HTMLTextToMarkdownLike(text string) string {
	root, err := html.Parse(strings.NewReader(text))
	if err != nil {
		return strings.TrimSpace(text)
	}
	var builder strings.Builder
	renderHTMLMarkdownNode(&builder, root)
	return cleanupMarkdownWhitespace(builder.String())
}

func renderHTMLMarkdownNode(builder *strings.Builder, node *html.Node) {
	if node == nil {
		return
	}
	switch node.Type {
	case html.TextNode:
		appendMarkdownText(builder, node.Data)
		return
	case html.CommentNode:
		return
	case html.DocumentNode:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			renderHTMLMarkdownNode(builder, child)
		}
		return
	case html.ElementNode:
	default:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			renderHTMLMarkdownNode(builder, child)
		}
		return
	}

	tag := strings.ToLower(node.Data)
	if shouldSkipHTMLMarkdownNode(node, tag) {
		return
	}
	switch tag {
	case "br":
		ensureMarkdownNewline(builder)
		return
	case "p", "div", "section", "article", "main", "header", "footer", "aside", "blockquote", "table", "tr":
		ensureMarkdownBlock(builder)
	case "li":
		ensureMarkdownNewline(builder)
		builder.WriteString("- ")
	case "h1", "h2", "h3", "h4", "h5", "h6":
		ensureMarkdownBlock(builder)
		level := int(tag[1] - '0')
		builder.WriteString(strings.Repeat("#", level))
		builder.WriteString(" ")
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderHTMLMarkdownNode(builder, child)
	}
	switch tag {
	case "p", "div", "section", "article", "main", "header", "footer", "aside", "blockquote", "table", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6":
		ensureMarkdownBlock(builder)
	}
}

func shouldSkipHTMLMarkdownNode(node *html.Node, tag string) bool {
	switch tag {
	case "head", "script", "style", "noscript", "template", "svg", "canvas", "meta", "link", "base":
		return true
	}
	if _, ok := htmlAttr(node, "hidden"); ok {
		return true
	}
	if value, ok := htmlAttr(node, "aria-hidden"); ok && strings.EqualFold(strings.TrimSpace(value), "true") {
		return true
	}
	if value, ok := htmlAttr(node, "style"); ok && htmlStyleHides(value) {
		return true
	}
	return false
}

func htmlStyleHides(style string) bool {
	for _, declaration := range strings.Split(style, ";") {
		key, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.ToLower(strings.TrimSpace(value))
		switch key {
		case "display":
			if value == "none" {
				return true
			}
		case "visibility":
			if value == "hidden" || value == "collapse" {
				return true
			}
		}
	}
	return false
}

func htmlAttr(node *html.Node, key string) (string, bool) {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val, true
		}
	}
	return "", false
}

func appendMarkdownText(builder *strings.Builder, text string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	if needsMarkdownSpace(builder) {
		builder.WriteByte(' ')
	}
	builder.WriteString(strings.Join(fields, " "))
}

func needsMarkdownSpace(builder *strings.Builder) bool {
	if builder.Len() == 0 {
		return false
	}
	text := builder.String()
	runes := []rune(text)
	last := runes[len(runes)-1]
	return !unicode.IsSpace(last) && last != '(' && last != '[' && last != '-'
}

func ensureMarkdownNewline(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	text := builder.String()
	if !strings.HasSuffix(text, "\n") {
		builder.WriteByte('\n')
	}
}

func ensureMarkdownBlock(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	text := builder.String()
	if strings.HasSuffix(text, "\n\n") {
		return
	}
	if strings.HasSuffix(text, "\n") {
		builder.WriteByte('\n')
		return
	}
	builder.WriteString("\n\n")
}

func cleanupMarkdownWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	punctuation := strings.NewReplacer(" .", ".", " ,", ",", " ;", ";", " :", ":", " !", "!", " ?", "?")
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = punctuation.Replace(line)
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
