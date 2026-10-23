package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// ErrDataInvalid reports a Markdown view body that is not usable data. The
// template itself may still be valid.
var ErrDataInvalid = errors.New("invalid view data")

// MaxMarkdownDataBytes bounds one Markdown view body. The prompt rules in
// agent-prompt.yml state the same limit to the model.
const MaxMarkdownDataBytes = 256 << 10

// MarkdownBlock is one closed ```view fence. Index counts closed view fences
// of a content block in order, including invalid ones, so the platform binding
// and the client segment parser agree on block identity.
type MarkdownBlock struct {
	Index     int
	Reference *Reference
	Data      json.RawMessage
	Err       error
}

// ParseMarkdownBlocks scans Markdown for top-level ```view fences.
//
// Grammar (mirrored by the WebClient parser):
//   - fences start at column 0; ``` or ~~~ runs of three or more open a fence;
//   - a view fence is exactly three backticks followed by the word "view"
//     (case-insensitive) and then whitespace or end of line;
//   - its info string accepts only key=<connectorId>/<viewKey>;
//   - a fence closes on a line with only the same character repeated at least
//     as many times, optionally followed by spaces or tabs;
//   - view fences inside other fences are code, not views;
//   - while streaming (final=false) the last line counts only once terminated
//     by a newline; an unclosed fence is never a block.
func ParseMarkdownBlocks(text string, final bool) []MarkdownBlock {
	lines := strings.Split(text, "\n")
	if !final {
		// The unterminated tail may still grow into a different line.
		lines = lines[:len(lines)-1]
	}
	var blocks []MarkdownBlock
	for i := 0; i < len(lines); i++ {
		char, width, info, ok := fenceOpen(lines[i])
		if !ok {
			continue
		}
		end := -1
		for j := i + 1; j < len(lines); j++ {
			if fenceClose(lines[j], char, width) {
				end = j
				break
			}
		}
		if end < 0 {
			break
		}
		if attrs, isView := viewInfo(char, width, info); isView {
			block := MarkdownBlock{Index: len(blocks)}
			block.Reference, block.Err = parseViewAttributes(attrs)
			if block.Err == nil {
				block.Data, block.Err = parseMarkdownData(strings.Join(lines[i+1:end], "\n"))
			}
			blocks = append(blocks, block)
		}
		i = end
	}
	return blocks
}

func fenceOpen(line string) (byte, int, string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, "", false
	}
	char, width := line[0], 0
	for width < len(line) && line[width] == char {
		width++
	}
	if width < 3 {
		return 0, 0, "", false
	}
	return char, width, line[width:], true
}

func fenceClose(line string, char byte, width int) bool {
	line = strings.TrimRight(line, " \t\r")
	if len(line) < width {
		return false
	}
	for i := 0; i < len(line); i++ {
		if line[i] != char {
			return false
		}
	}
	return true
}

func viewInfo(char byte, width int, info string) (string, bool) {
	if char != '`' || width != 3 || len(info) < 4 || !strings.EqualFold(info[:4], "view") {
		return "", false
	}
	rest := info[4:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return rest, true
}

func parseViewAttributes(attrs string) (*Reference, error) {
	var key string
	for _, field := range strings.Fields(attrs) {
		name, value, ok := strings.Cut(field, "=")
		if !ok || name != "key" || key != "" {
			return nil, fmt.Errorf("%w: view block accepts only key=<connectorId>/<viewKey>", ErrInvalid)
		}
		key = value
	}
	connectorID, viewKey, ok := strings.Cut(key, "/")
	if !ok || !idPattern.MatchString(connectorID) || !idPattern.MatchString(viewKey) {
		return nil, fmt.Errorf("%w: view block key must be <connectorId>/<viewKey>", ErrInvalid)
	}
	return &Reference{Source: "connector", ConnectorID: connectorID, Key: viewKey}, nil
}

func parseMarkdownData(body string) (json.RawMessage, error) {
	data := bytes.TrimSpace([]byte(body))
	if len(data) > MaxMarkdownDataBytes {
		return nil, fmt.Errorf("%w: body exceeds %d bytes", ErrDataInvalid, MaxMarkdownDataBytes)
	}
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') || !json.Valid(data) {
		return nil, fmt.Errorf("%w: body must be one JSON object or array", ErrDataInvalid)
	}
	return json.RawMessage(data), nil
}

func compileDataSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	if len(raw) > MaxDataSchemaBytes {
		return nil, fmt.Errorf("exceeds %d bytes", MaxDataSchemaBytes)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
}

// ValidateData checks a Markdown view body against the definition's schema.
func ValidateData(d Definition, data json.RawMessage) error {
	if len(d.DataSchema) == 0 {
		return nil
	}
	resolved, err := compileDataSchema(d.DataSchema)
	if err != nil {
		return fmt.Errorf("%w: dataSchema", ErrInvalid)
	}
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return fmt.Errorf("%w: %v", ErrDataInvalid, err)
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("%w: %v", ErrDataInvalid, err)
	}
	return nil
}

// ErrorCode maps a resolution failure to the stable public viewError code.
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDataInvalid):
		return "view_data_invalid"
	case errors.Is(err, ErrNotFound):
		return "view_not_found"
	case errors.Is(err, ErrInvalid):
		return "invalid_view"
	default:
		return "view_unavailable"
	}
}
