package conversationexport

import (
	"mime"
	"path"
	"regexp"
	"strings"

	"agent-platform/internal/chatresource"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Discover destinations from Markdown nodes, never from code or raw HTML.
func markdownMediaRefs(source string) []string {
	document := mediaParser.Parse(text.NewReader([]byte(source)))
	var refs []string
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var target []byte
		image := false
		switch node := node.(type) {
		case *ast.Image:
			target, image = node.Destination, true
		case *ast.Link:
			target = node.Destination
		default:
			return ast.WalkContinue, nil
		}
		target = util.UnescapePunctuations(target)
		target = util.ResolveNumericReferences(target)
		target = util.ResolveEntityNames(target)
		if len(target) > 0 && (image || chatresource.IsMedia(mime.TypeByExtension(path.Ext(string(target))))) {
			refs = append(refs, string(target))
		}
		if image {
			return ast.WalkSkipChildren, nil // Image alt text does not load nested media.
		}
		return ast.WalkContinue, nil
	})
	return refs
}

// Match the client's local-media extension inside the Markdown parser, so code
// spans, fenced blocks and image alt text keep the parser's normal boundaries.
var localMediaPattern = regexp.MustCompile(`(?i)^!\[[^\[\]\n]*\]\(((@chat/|@workspace/|/)[^()<>\r\n]+)\)`)
var localMediaSuffix = regexp.MustCompile(`(?i)\.(avif|bmp|gif|ico|jpe?g|png|svg|webp|aac|m4a|mp3|oga|ogg|opus|wav|weba|m4v|mov|mp4|mpeg|mpg|ogv|webm)$`)
var mediaParser = goldmark.New(goldmark.WithParserOptions(parser.WithInlineParsers(util.Prioritized(localMediaParser{}, 150)))).Parser()

type localMediaParser struct{}

func (localMediaParser) Trigger() []byte { return []byte{'!'} }
func (localMediaParser) Parse(_ ast.Node, reader text.Reader, _ parser.Context) ast.Node {
	line, _ := reader.PeekLine()
	match := localMediaPattern.FindSubmatch(line)
	if match == nil {
		return nil
	}
	href := strings.TrimSpace(string(match[1]))
	if !strings.ContainsAny(href, " \t") || strings.HasPrefix(href, "//") || !localMediaSuffix.MatchString(href) {
		return nil
	}
	link := ast.NewLink()
	link.Destination = []byte(href)
	reader.Advance(len(match[0]))
	return ast.NewImage(link)
}
