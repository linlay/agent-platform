package bashast

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const parseTimeout = 50 * time.Millisecond

func ParseForSecurity(command string) ParseResult {
	return ParseForSecurityWithKnownVariables(command, nil)
}

func ParseForSecurityWithKnownVariables(command string, variables map[string]string) ParseResult {
	if ok, reason := runPrechecks(command); !ok {
		return ParseResult{Kind: TooComplex, Reason: reason, NodeType: "precheck"}
	}

	type parseResponse struct {
		file *syntax.File
		err  error
	}
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()

	ch := make(chan parseResponse, 1)
	go func() {
		parser := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBash))
		file, err := parser.Parse(strings.NewReader(command), "")
		ch <- parseResponse{file: file, err: err}
	}()

	var response parseResponse
	select {
	case <-ctx.Done():
		return ParseResult{Kind: TooComplex, Reason: "parser timeout", NodeType: "parser"}
	case response = <-ch:
	}

	if response.err != nil {
		return ParseResult{Kind: TooComplex, Reason: fmt.Sprintf("parse error: %v", response.err), NodeType: "parser"}
	}
	if response.file == nil {
		return ParseResult{Kind: ParseUnavailable, Reason: "parser returned nil file", NodeType: "parser"}
	}

	w := newWalkerWithKnownVariables(command, variables)
	if err := w.walkFile(response.file); err != nil {
		return ParseResult{Kind: TooComplex, Reason: err.reason, NodeType: err.nodeType}
	}
	return ParseResult{Kind: Simple, Commands: w.commands}
}

func ParseWithEmbeddedDetection(command string) (ParseResult, []EmbeddedScript) {
	return ParseWithEmbeddedDetectionAndKnownVariables(command, nil)
}

func ParseWithEmbeddedDetectionAndKnownVariables(command string, variables map[string]string) (ParseResult, []EmbeddedScript) {
	result := ParseForSecurityWithKnownVariables(command, variables)
	if result.Kind != Simple {
		return result, nil
	}
	return result, DetectEmbeddedScripts(result.Commands)
}
