package bashast

import (
	"strconv"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// maxBraceWords bounds static brace expansion. Larger expansions are reviewed
// as too complex instead of allocating an attacker-controlled word list.
const maxBraceWords = 128

// expandBraceWord performs Bash brace expansion for a command argument so the
// reviewed argv matches what the shell executes. It returns the original word
// when the word contains no valid brace expression.
func expandBraceWord(word *syntax.Word) ([]*syntax.Word, *walkError) {
	if word == nil {
		return nil, nil
	}
	clone := cloneWordParts(word)
	if !syntax.SplitBraces(clone) {
		return []*syntax.Word{word}, nil
	}
	count, ok := braceWordCount(clone, 0)
	if !ok || count > maxBraceWords {
		return nil, tooComplex("syntax.BraceExp", "brace expansion exceeds %d reviewed words", maxBraceWords)
	}
	return expand.Braces(clone), nil
}

func cloneWordParts(word *syntax.Word) *syntax.Word {
	clone := *word
	clone.Parts = append([]syntax.WordPart(nil), word.Parts...)
	return &clone
}

func braceWordCount(word *syntax.Word, depth int) (int, bool) {
	if depth > 8 {
		return 0, false
	}
	total := 1
	for _, part := range word.Parts {
		brace, ok := part.(*syntax.BraceExp)
		if !ok {
			continue
		}
		count := 0
		if brace.Sequence {
			n, ok := braceSequenceCount(brace)
			if !ok {
				return 0, false
			}
			count = n
		} else {
			for _, elem := range brace.Elems {
				n, ok := braceWordCount(elem, depth+1)
				if !ok {
					return 0, false
				}
				count += n
				if count > maxBraceWords {
					return count, true
				}
			}
		}
		total *= count
		if total > maxBraceWords {
			return total, true
		}
	}
	return total, true
}

func braceSequenceCount(brace *syntax.BraceExp) (int, bool) {
	if len(brace.Elems) < 2 {
		return 0, false
	}
	from, to := brace.Elems[0].Lit(), brace.Elems[1].Lit()
	start, err1 := strconv.Atoi(from)
	end, err2 := strconv.Atoi(to)
	if err1 != nil || err2 != nil {
		if len(from) != 1 || len(to) != 1 {
			return 0, false
		}
		start, end = int(from[0]), int(to[0])
	}
	step := 1
	if len(brace.Elems) > 2 {
		n, err := strconv.Atoi(brace.Elems[2].Lit())
		if err != nil {
			return 0, false
		}
		if n < 0 {
			n = -n
		}
		if n > 0 {
			step = n
		}
	}
	span := end - start
	if span < 0 {
		span = -span
	}
	return span/step + 1, true
}
