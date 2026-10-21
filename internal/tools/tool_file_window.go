package tools

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
)

// fileWindowScan is one sequential pass over a file: the version hash, line
// count and UTF-8 validity of the whole file, plus the raw bytes of the
// requested line window. Hashing the same bytes that are delivered keeps the
// read proof consistent with the content the model saw.
type fileWindowScan struct {
	SHA256     string
	TotalLines int
	UTF8       bool
	Window     []byte
	// WindowCut reports that maxBytes stopped capture, so the last captured
	// line may be incomplete.
	WindowCut bool
}

// scanFileWindow reads lines startLine.. (at most limit lines when limit > 0)
// while hashing the whole file. All supported text encodings are ASCII
// compatible and never use 0x0A inside a multi-byte sequence, so splitting
// raw bytes on '\n' yields the same lines as the decoded text.
func scanFileWindow(reader io.Reader, startLine int, limit int, maxBytes int) (fileWindowScan, error) {
	if startLine < 1 {
		startLine = 1
	}
	hash := sha256.New()
	validator := utf8StreamValidator{valid: true}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	var scan fileWindowScan
	line := 1
	newlines := 0
	var size int64
	var lastByte byte
	capturing := true
	for {
		chunk, err := buffered.ReadSlice('\n')
		if len(chunk) > 0 {
			hash.Write(chunk)
			validator.write(chunk)
			size += int64(len(chunk))
			lastByte = chunk[len(chunk)-1]
			if capturing && line >= startLine && (limit <= 0 || line < startLine+limit) {
				room := maxBytes - len(scan.Window)
				if len(chunk) > room {
					scan.Window = append(scan.Window, chunk[:max(room, 0)]...)
					scan.WindowCut = true
					capturing = false
				} else {
					scan.Window = append(scan.Window, chunk...)
				}
			}
			if lastByte == '\n' {
				newlines++
				line++
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fileWindowScan{}, err
		}
	}
	scan.TotalLines = newlines
	if size > 0 && lastByte != '\n' {
		scan.TotalLines++
	}
	scan.SHA256 = hex.EncodeToString(hash.Sum(nil))
	scan.UTF8 = validator.done()
	return scan, nil
}

type utf8StreamValidator struct {
	valid   bool
	pending []byte
}

func (v *utf8StreamValidator) write(chunk []byte) {
	if !v.valid {
		return
	}
	data := chunk
	if len(v.pending) > 0 {
		data = append(v.pending, chunk...)
	}
	end := len(data)
	for index := len(data) - 1; index >= 0 && index >= len(data)-utf8.UTFMax; index-- {
		if utf8.RuneStart(data[index]) {
			if !utf8.FullRune(data[index:]) {
				end = index
			}
			break
		}
	}
	if !utf8.Valid(data[:end]) {
		v.valid = false
		return
	}
	v.pending = append(v.pending[:0:0], data[end:]...)
}

func (v *utf8StreamValidator) done() bool {
	return v.valid && len(v.pending) == 0
}

// filePage is the part of a decoded window that fits the model budget.
type filePage struct {
	Content string
	// Lines counts complete lines included in Content.
	Lines int
	// LongLineTruncated is set when the first line alone exceeded the budget
	// and Content holds only its prefix; that line is not part of coverage.
	LongLineTruncated bool
}

// fitFilePage keeps whole lines of text while their JSON-escaped size fits
// budget. lastLineComplete=false marks the final line of text as cut by the
// raw read limit.
func fitFilePage(text string, startLine int, totalLines int, lineNumbered bool, lastLineComplete bool, budget int) filePage {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	width := max(len(fmt.Sprintf("%d", max(totalLines, startLine))), 6)
	var builder strings.Builder
	used := 0
	page := filePage{}
	for index, line := range lines {
		piece := line
		if lineNumbered {
			piece = fmt.Sprintf("%*d\t%s", width, startLine+index, line)
		}
		cost := ModelJSONStringLen(piece)
		complete := lastLineComplete || index < len(lines)-1
		if complete && used+cost <= budget {
			builder.WriteString(piece)
			used += cost
			page.Lines++
			continue
		}
		if page.Lines == 0 {
			builder.WriteString(truncateToModelJSONBudget(piece, budget))
			page.LongLineTruncated = true
		}
		break
	}
	page.Content = builder.String()
	return page
}

func truncateToModelJSONBudget(text string, budget int) string {
	used := 0
	for index := 0; index < len(text); {
		_, size := utf8.DecodeRuneInString(text[index:])
		cost := ModelJSONStringLen(text[index : index+size])
		if used+cost > budget {
			return text[:index]
		}
		used += cost
		index += size
	}
	return text
}
