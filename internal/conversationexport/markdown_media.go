package conversationexport

import (
	"mime"
	"path"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Discover destinations from Markdown nodes, never from code or raw HTML.
func markdownMediaRefs(source string) []string {
	document := goldmark.DefaultParser().Parse(text.NewReader([]byte(source)))
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
		if len(target) > 0 && (image || isSnapshotMedia(mime.TypeByExtension(path.Ext(string(target))))) {
			refs = append(refs, string(target))
		}
		if image {
			return ast.WalkSkipChildren, nil // Image alt text does not load nested media.
		}
		return ast.WalkContinue, nil
	})
	return refs
}
