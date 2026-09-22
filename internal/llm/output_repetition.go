package llm

import (
	"unicode/utf8"

	"agent-platform/internal/apperrors"
)

// These limits count Unicode code points, not bytes, tokens or SSE frames.
// Each provider attempt owns separate detectors for content and reasoning.
const (
	outputRepeatStartChars = 4000
	outputRepeatCheckEvery = 256
	outputRepeatWindow     = 2048
	outputRepeatMinBlock   = 8
	outputRepeatMaxBlock   = 256
	outputRepeatMinCount   = 8
	outputRepeatMinChars   = 1024
)

type outputRepetitionDetector struct {
	tail    [outputRepeatWindow]rune
	total   int
	partial string // THINK_TAG_CONTENT can split a UTF-8 character across deltas.
}

type outputRepetitionMatch struct {
	blockChars int
	count      int
}

func (d *outputRepetitionDetector) append(text string) (outputRepetitionMatch, bool) {
	if d.partial != "" {
		text = d.partial + text
		d.partial = ""
	}
	for len(text) > 0 {
		if !utf8.FullRuneInString(text) {
			d.partial = text
			break
		}
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		d.tail[d.total%outputRepeatWindow] = r
		d.total++
		if d.total < outputRepeatStartChars || (d.total-outputRepeatStartChars)%outputRepeatCheckEvery != 0 {
			continue
		}
		// Check at character boundaries even within a large frame, so detection
		// does not depend on how the provider splits its SSE deltas.
		var tail [outputRepeatWindow]rune
		start := d.total % outputRepeatWindow
		n := copy(tail[:], d.tail[start:])
		copy(tail[n:], d.tail[:start])
		for block := outputRepeatMinBlock; block <= outputRepeatMaxBlock; block++ {
			count := 1
			for end := len(tail) - block; end >= block; end -= block {
				same := true
				for i := 0; i < block; i++ {
					if tail[end-block+i] != tail[len(tail)-block+i] {
						same = false
						break
					}
				}
				if !same {
					break
				}
				count++
			}
			if count >= outputRepeatMinCount && count*block >= outputRepeatMinChars {
				return outputRepetitionMatch{blockChars: block, count: count}, true
			}
		}
	}
	return outputRepetitionMatch{}, false
}

func (s *llmRunStream) detectOutputRepetition(text, channel string, detector *outputRepetitionDetector) bool {
	match, repeated := detector.append(text)
	if !repeated {
		return false
	}
	turn := s.currentTurn
	turn.outputGuardErr = apperrors.New(apperrors.CodeModelOutputRepetition,
		"检测到模型持续重复输出，已停止本次生成。此前已完成的操作已保留。",
		apperrors.WithDiagnostics(map[string]any{
			"channel": channel, "unit": "unicode_code_points",
			"startChars": outputRepeatStartChars, "detectedAtChars": detector.total,
			"blockChars": match.blockChars, "repeatCount": match.count,
			"repeatedChars": match.blockChars * match.count,
		}))
	turn.observation.CompletionTrigger = "output_repetition"
	// Cancel only this provider request; the Run still owns error/discard
	// persistence. Do not signal user interruption or retry this attempt.
	if turn.cancel != nil {
		turn.cancel()
	}
	if turn.body != nil {
		_ = turn.body.Close()
	}
	if turn.trace != nil {
		turn.trace.complete("error", turn.outputGuardErr.Error(), turn.content.String(), turn.reasoning.String(), nil, "", turn.usage, nil)
	}
	return true
}
