package types

// ValueSource binds an extracted value by parser position, never by searching
// for equal argument strings. Invalid or generated sources remain nonliteral.
func ValueSource(input CommandInput, ref ValueRef, derived bool) SourceWord {
	out := SourceWord{Arg: -1}
	if !ref.Known || ref.Index < 0 || ref.Index >= len(input.Args) || len(input.Words) != len(input.Args) {
		return out
	}
	out = input.Words[ref.Index]
	out.ValueStart, out.ValueEnd = 0, 0
	arg := input.Args[ref.Index]
	if derived || !out.Literal || out.Origin != WordOriginSyntax || ref.Start < 0 || ref.End < ref.Start || ref.End > len(arg) {
		out.Literal = false
		return out
	}
	cooked, offsets, ok := literalOffsets(out.Raw)
	if !ok || cooked != arg || ref.Start == ref.End {
		out.Literal = false
		return out
	}
	out.ValueStart = out.Start + offsets[ref.Start]
	out.ValueEnd = out.Start + offsets[ref.End-1] + 1
	return out
}

// literalOffsets performs quote removal only on syntax-confirmed literal words.
// offsets index UTF-8 bytes, matching the Bash parser's source positions.
func literalOffsets(raw string) (string, []int, bool) {
	var value []byte
	var offsets []int
	quote := byte(0)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quote == 0 && (c == '\'' || c == '"') {
			quote = c
			continue
		}
		if c == quote && quote != 0 {
			quote = 0
			continue
		}
		if c == '\\' && quote != '\'' {
			if i+1 >= len(raw) {
				return "", nil, false
			}
			n := raw[i+1]
			if quote == 0 || n == '$' || n == '`' || n == '"' || n == '\\' || n == '\n' {
				i++
				if n == '\n' {
					continue
				}
				c = n
			}
		}
		value = append(value, c)
		offsets = append(offsets, i)
	}
	return string(value), offsets, quote == 0 && len(raw) > 0
}
