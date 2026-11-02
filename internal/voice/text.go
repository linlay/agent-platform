package voice

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// speechText removes presentation syntax only; the committed Chat stays intact.
func speechText(body string) string {
	source := []byte(body)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	var out strings.Builder
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock, *ast.RawHTML, *ast.Image, *ast.AutoLink, *extast.Table:
			if entering {
				return ast.WalkSkipChildren, nil
			}
		}
		if entering {
			switch n := n.(type) {
			case *ast.Text:
				out.Write(n.Segment.Value(source))
				if n.SoftLineBreak() || n.HardLineBreak() {
					out.WriteByte(' ')
				}
			case *ast.String:
				out.Write(n.Value)
			}
		} else if n.Type() == ast.TypeBlock {
			out.WriteByte(' ')
		}
		return ast.WalkContinue, nil
	})
	plain := strings.Join(strings.Fields(out.String()), " ")
	if plain == "" {
		return "回答包含需要在界面查看的内容。"
	}
	runes := []rune(plain)
	if len(runes) > 2000 {
		return string(runes[:2000]) + "其余内容请查看完整聊天。"
	}
	return plain
}
