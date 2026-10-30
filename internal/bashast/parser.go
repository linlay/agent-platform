package bashast

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

const parseTimeout = 50 * time.Millisecond

var errParserTimeout = errors.New("parser timeout")

func ParseForSecurityWithKnownVariables(command string, variables map[string]string) ParseResult {
	if ok, reason := runPrechecks(command); !ok {
		return ParseResult{Kind: TooComplex, Reason: reason, NodeType: "precheck"}
	}
	return ParseForExecution(command, variables)
}

// ParseForExecution only identifies shell structure. Callers must still run
// security checks on every part not covered by a verified execution grant.
func ParseForExecution(command string, variables map[string]string) ParseResult {
	file, err := parseFile(command)
	if err != nil {
		reason := fmt.Sprintf("parse error: %v", err)
		if errors.Is(err, errParserTimeout) {
			reason = err.Error()
		}
		return ParseResult{Kind: TooComplex, Reason: reason, NodeType: "parser"}
	}
	if file == nil {
		return ParseResult{Kind: ParseUnavailable, Reason: "parser returned nil file", NodeType: "parser"}
	}

	w := newWalkerWithKnownVariables(command, variables)
	if err := w.walkFile(file); err != nil {
		return ParseResult{Kind: TooComplex, Reason: err.reason, NodeType: err.nodeType}
	}
	return ParseResult{Kind: Simple, Commands: w.commands}
}

// parseFile shares the same bounded Bash parser between structural analysis
// and quote-aware text checks; neither path executes the supplied command.
func parseFile(command string) (*syntax.File, error) {
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
		return nil, errParserTimeout
	case response = <-ch:
	}
	return response.file, response.err
}
