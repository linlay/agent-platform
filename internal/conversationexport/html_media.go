package conversationexport

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

func htmlMediaRefs(reader io.Reader) ([]string, error) {
	tokenizer := html.NewTokenizer(reader)
	var refs []string
	seen := map[string]bool{}
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				return refs, nil
			}
			return nil, tokenizer.Err()
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data != "img" && token.Data != "video" && token.Data != "audio" && token.Data != "source" {
			continue
		}
		for _, attr := range token.Attr {
			if attr.Key != "src" && !(attr.Key == "poster" && token.Data == "video") && !(attr.Key == "srcset" && (token.Data == "img" || token.Data == "source")) {
				continue
			}
			values := []string{attr.Val}
			if attr.Key == "srcset" {
				values = srcsetURLs(attr.Val)
			}
			for _, value := range values {
				if value != "" && !seen[value] {
					seen[value] = true
					refs = append(refs, value)
				}
			}
		}
	}
}

func srcsetURLs(value string) []string {
	var urls []string
	for value != "" {
		value = strings.TrimLeft(value, " \t\r\n\f,")
		if value == "" {
			break
		}
		end := strings.IndexAny(value, " \t\r\n\f")
		if end < 0 {
			end = len(value)
		}
		target := value[:end]
		value = value[end:]
		urls = append(urls, strings.TrimRight(target, ","))
		if strings.HasSuffix(target, ",") {
			continue
		}
		if end = strings.Index(value, ","); end >= 0 {
			value = value[end+1:]
		} else {
			value = ""
		}
	}
	return urls
}
