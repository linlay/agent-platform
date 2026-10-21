package contracts

import (
	"sort"
	"strings"
)

// LineEdit describes one replaced region in pre-edit line coordinates: the
// region spanned lines Start..End and the replacement changes the line count
// by Delta.
type LineEdit struct {
	Start int
	End   int
	Delta int
}

// CountTextLines counts lines the way file_read pages them: every "\n" ends a
// line and a non-empty trailing fragment without "\n" is one more line.
func CountTextLines(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}

// NormalizeLineRanges drops invalid ranges, sorts them and merges overlapping
// or adjacent ones. The result never aliases the input.
func NormalizeLineRanges(ranges []LineRange) []LineRange {
	out := make([]LineRange, 0, len(ranges))
	for _, item := range ranges {
		if item.Start >= 1 && item.End >= item.Start {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:1]
	for _, item := range out[1:] {
		last := &merged[len(merged)-1]
		if item.Start <= last.End+1 {
			if item.End > last.End {
				last.End = item.End
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

// ClampLineRanges limits ranges to 1..totalLines.
func ClampLineRanges(ranges []LineRange, totalLines int) []LineRange {
	out := make([]LineRange, 0, len(ranges))
	for _, item := range ranges {
		if item.End > totalLines {
			item.End = totalLines
		}
		out = append(out, item)
	}
	return NormalizeLineRanges(out)
}

// ApplyLineEdits maps covered ranges onto the post-edit file. Each replaced
// region joins the range that contains it, so lines the model just wrote stay
// covered; ranges after a region shift by its Delta.
func ApplyLineEdits(ranges []LineRange, edits []LineEdit) []LineRange {
	out := NormalizeLineRanges(ranges)
	ordered := append([]LineEdit(nil), edits...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start > ordered[j].Start })
	for _, edit := range ordered {
		for index := range out {
			item := &out[index]
			switch {
			case item.End < edit.Start:
			case item.Start > edit.End:
				item.Start += edit.Delta
				item.End += edit.Delta
			default:
				item.Start = min(item.Start, edit.Start)
				item.End = max(max(item.End, edit.End)+edit.Delta, edit.Start)
			}
		}
	}
	return NormalizeLineRanges(out)
}

// Clone returns a snapshot that shares no slices with s.
func (s ReadFileSnapshot) Clone() ReadFileSnapshot {
	s.Ranges = append([]LineRange(nil), s.Ranges...)
	if len(s.Ranges) == 0 {
		s.Ranges = nil
	}
	return s
}

// Covers reports whether every line in start..end was delivered.
func (s ReadFileSnapshot) Covers(start int, end int) bool {
	return len(s.Uncovered(start, end)) == 0
}

// Complete reports whether the whole file version was delivered.
func (s ReadFileSnapshot) Complete() bool {
	return s.TotalLines <= 0 || s.Covers(1, s.TotalLines)
}

// Uncovered returns the parts of start..end that were not delivered.
func (s ReadFileSnapshot) Uncovered(start int, end int) []LineRange {
	if end < start {
		return nil
	}
	var missing []LineRange
	next := start
	for _, item := range NormalizeLineRanges(s.Ranges) {
		if item.End < next {
			continue
		}
		if item.Start > end {
			break
		}
		if item.Start > next {
			missing = append(missing, LineRange{Start: next, End: item.Start - 1})
		}
		next = item.End + 1
		if next > end {
			return missing
		}
	}
	return append(missing, LineRange{Start: next, End: end})
}

// MergeReadFileSnapshot combines two proofs for the same path. Proofs of the
// same version accumulate their ranges; otherwise the later observation wins.
func MergeReadFileSnapshot(prev ReadFileSnapshot, next ReadFileSnapshot) ReadFileSnapshot {
	if strings.TrimSpace(prev.SHA256) == "" {
		return next.Clone()
	}
	if strings.TrimSpace(next.SHA256) == "" {
		return prev.Clone()
	}
	if prev.SHA256 != next.SHA256 || prev.TotalLines != next.TotalLines {
		if prev.ReadAtUnixMs > next.ReadAtUnixMs {
			return prev.Clone()
		}
		return next.Clone()
	}
	latest, other := next, prev
	if prev.ReadAtUnixMs > next.ReadAtUnixMs {
		latest, other = prev, next
	}
	merged := latest.Clone()
	merged.Ranges = NormalizeLineRanges(append(append([]LineRange(nil), latest.Ranges...), other.Ranges...))
	return merged
}
