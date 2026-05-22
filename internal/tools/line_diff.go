package tools

import (
	"sort"
	"strings"

	. "agent-platform/internal/contracts"
)

const maxExactLineDiffCells = 8_000_000

func computeLineDiffStats(before string, after string) LineDiffStats {
	return diffLineSlices(splitDiffLines(before), splitDiffLines(after))
}

func lineStatsPayload(stats LineDiffStats) map[string]any {
	return map[string]any{
		"addedLines":   stats.AddedLines,
		"deletedLines": stats.DeletedLines,
		"editedLines":  stats.EditedLines,
	}
}

func splitDiffLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func diffLineSlices(before []string, after []string) LineDiffStats {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	before = before[prefix:]
	after = after[prefix:]

	suffix := 0
	for suffix < len(before) && suffix < len(after) &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	if suffix > 0 {
		before = before[:len(before)-suffix]
		after = after[:len(after)-suffix]
	}

	switch {
	case len(before) == 0 && len(after) == 0:
		return LineDiffStats{}
	case len(before) == 0:
		return LineDiffStats{AddedLines: len(after)}
	case len(after) == 0:
		return LineDiffStats{DeletedLines: len(before)}
	}

	cells := (len(before) + 1) * (len(after) + 1)
	if cells > maxExactLineDiffCells {
		return anchoredLineDiffStats(before, after)
	}

	return exactLineDiffStats(before, after)
}

func anchoredLineDiffStats(before []string, after []string) LineDiffStats {
	matches := uniqueLineMatches(before, after)
	if len(matches) <= 2 {
		added := len(after)
		deleted := len(before)
		return LineDiffStats{
			AddedLines:   added,
			DeletedLines: deleted,
			EditedLines:  minInt(added, deleted),
		}
	}

	var stats LineDiffStats
	done := linePair{}
	for _, match := range matches[1:] {
		segmentStats := diffLineSlices(before[done.x:match.x], after[done.y:match.y])
		stats.AddedLines += segmentStats.AddedLines
		stats.DeletedLines += segmentStats.DeletedLines
		stats.EditedLines += segmentStats.EditedLines
		done = linePair{x: match.x + 1, y: match.y + 1}
	}
	return stats
}

func exactLineDiffStats(before []string, after []string) LineDiffStats {
	rows := len(before) + 1
	cols := len(after) + 1
	lcs := make([]int, rows*cols)
	for i := len(before) - 1; i >= 0; i-- {
		for j := len(after) - 1; j >= 0; j-- {
			idx := i*cols + j
			if before[i] == after[j] {
				lcs[idx] = 1 + lcs[(i+1)*cols+j+1]
				continue
			}
			down := lcs[(i+1)*cols+j]
			right := lcs[i*cols+j+1]
			if down > right {
				lcs[idx] = down
			} else {
				lcs[idx] = right
			}
		}
	}

	var stats LineDiffStats
	hunkAdded := 0
	hunkDeleted := 0
	flushHunk := func() {
		if hunkAdded == 0 && hunkDeleted == 0 {
			return
		}
		stats.AddedLines += hunkAdded
		stats.DeletedLines += hunkDeleted
		stats.EditedLines += minInt(hunkAdded, hunkDeleted)
		hunkAdded = 0
		hunkDeleted = 0
	}

	i, j := 0, 0
	for i < len(before) || j < len(after) {
		if i < len(before) && j < len(after) && before[i] == after[j] {
			flushHunk()
			i++
			j++
			continue
		}
		if j < len(after) && (i == len(before) || lcs[i*cols+j+1] >= lcs[(i+1)*cols+j]) {
			hunkAdded++
			j++
			continue
		}
		hunkDeleted++
		i++
	}
	flushHunk()
	return stats
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

type linePair struct {
	x int
	y int
}

func uniqueLineMatches(before []string, after []string) []linePair {
	counts := make(map[string]int)
	for _, line := range before {
		if counts[line] > -2 {
			counts[line]--
		}
	}
	for _, line := range after {
		if counts[line] > -8 {
			counts[line] -= 4
		}
	}

	var beforeIndexes []int
	var afterIndexes []int
	var inverse []int
	for idx, line := range after {
		if counts[line] == -1+-4 {
			counts[line] = len(afterIndexes)
			afterIndexes = append(afterIndexes, idx)
		}
	}
	for idx, line := range before {
		if afterIndex, ok := counts[line]; ok && afterIndex >= 0 {
			beforeIndexes = append(beforeIndexes, idx)
			inverse = append(inverse, afterIndex)
		}
	}

	n := len(beforeIndexes)
	thresholds := make([]int, n)
	lengths := make([]int, n)
	for i := range thresholds {
		thresholds[i] = n + 1
	}
	for i, afterIndex := range inverse {
		k := sort.Search(n, func(k int) bool {
			return thresholds[k] >= afterIndex
		})
		thresholds[k] = afterIndex
		lengths[i] = k + 1
	}

	longest := 0
	for _, length := range lengths {
		if longest < length {
			longest = length
		}
	}
	matches := make([]linePair, 2+longest)
	matches[1+longest] = linePair{x: len(before), y: len(after)}
	lastAfter := n
	for i := n - 1; i >= 0; i-- {
		if lengths[i] == longest && inverse[i] < lastAfter {
			matches[longest] = linePair{x: beforeIndexes[i], y: afterIndexes[inverse[i]]}
			longest--
		}
	}
	matches[0] = linePair{}
	return matches
}
