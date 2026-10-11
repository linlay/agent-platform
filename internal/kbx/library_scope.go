package kbx

import (
	"fmt"
	"path"
	"strings"

	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
)

func allowedDocumentPath(l library, uri string) (string, error) {
	collection, relative, ok := kbases.DocumentReference(uri)
	if !ok {
		return "", fmt.Errorf("invalid KBX document reference")
	}
	for _, c := range l.definition.Collections {
		if c.Name == collection && knowledge.IndexedPathAllowed(relative, c.Include, append(append([]string{}, c.Exclude...), ".kbx-platform/**")) {
			return collection + "/" + relative, nil
		}
	}
	return "", unavailable("KBX returned a document outside the bound library scope")
}
func collectionPredicate(name string) map[string]any {
	return map[string]any{"op": "eq", "key": "sys.collection", "value": name}
}
func scopedPathPredicate(l library, op, value string) (any, error) {
	if strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00") {
		return nil, fmt.Errorf("knowledge paths must use collection/relativePath")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return nil, fmt.Errorf("path must not traverse parents")
		}
	}
	collection, relative, hasSlash := strings.Cut(value, "/")
	branches := []any{}
	for _, c := range l.definition.Collections {
		match := collection == c.Name
		if op == "pathGlob" {
			var err error
			match, err = path.Match(collection, c.Name)
			if err != nil {
				return nil, err
			}
			if collection == "**" {
				match = true
				relative = value
			}
		}
		if !match {
			continue
		}
		items := []any{collectionPredicate(c.Name)}
		if hasSlash && relative != "" {
			items = append(items, predicate(op, relative))
		} else if op == "pathGlob" {
			return nil, fmt.Errorf("pathGlob must include a collection prefix, e.g. ai/**/*.md or **/*.md")
		}
		branches = append(branches, map[string]any{"op": "and", "args": items})
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("path does not select a collection in the bound library")
	}
	return map[string]any{"op": "or", "args": branches}, nil
}
func libraryPolicy(l library) any {
	branches := []any{}
	for _, c := range l.definition.Collections {
		terms := []any{collectionPredicate(c.Name)}
		if len(c.Include) > 0 {
			include := []any{}
			for _, p := range c.Include {
				include = append(include, predicate("pathGlob", p))
			}
			terms = append(terms, map[string]any{"op": "or", "args": include})
		}
		for _, p := range append(append([]string{}, c.Exclude...), ".kbx-platform/**") {
			terms = append(terms, map[string]any{"op": "not", "arg": predicate("pathGlob", p)})
		}
		branches = append(branches, map[string]any{"op": "and", "args": terms})
	}
	return map[string]any{"op": "or", "args": branches}
}
