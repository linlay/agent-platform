package tools

import (
	"fmt"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	textencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

type decodedFileText struct {
	Content  string
	Encoding string
}

type fileTextEncoding struct {
	Label string
	Codec textencoding.Encoding
}

func decodeFileText(data []byte, requestedEncoding string) (decodedFileText, bool, error) {
	if strings.TrimSpace(requestedEncoding) != "" {
		encoding, ok := lookupFileTextEncoding(requestedEncoding)
		if !ok {
			return decodedFileText{}, false, fmt.Errorf("unsupported encoding: %s", requestedEncoding)
		}
		text, err := decodeBytesWithFileEncoding(data, encoding)
		if err != nil {
			return decodedFileText{}, false, err
		}
		if !looksLikeDecodedText(text) {
			return decodedFileText{}, false, fmt.Errorf("decoded content is not text")
		}
		return decodedFileText{Content: text, Encoding: encoding.Label}, true, nil
	}
	if utf8.Valid(data) {
		return decodedFileText{Content: string(data), Encoding: "utf-8"}, true, nil
	}
	if !looksLikeTextBytes(data) {
		return decodedFileText{}, false, nil
	}
	bestScore := -1
	var best decodedFileText
	for _, encoding := range fileTextEncodingCandidates() {
		text, err := decodeBytesWithFileEncoding(data, encoding)
		if err != nil || !looksLikeDecodedText(text) {
			continue
		}
		score := decodedTextScore(text)
		if score > bestScore {
			bestScore = score
			best = decodedFileText{Content: text, Encoding: encoding.Label}
		}
	}
	if bestScore < 0 {
		return decodedFileText{}, false, nil
	}
	return best, true, nil
}

func encodeFileText(content string, encodingName string) ([]byte, string, error) {
	encoding, ok := lookupFileTextEncoding(encodingName)
	if !ok {
		return nil, "", fmt.Errorf("unsupported encoding: %s", encodingName)
	}
	if encoding.Label == "utf-8" {
		return []byte(content), encoding.Label, nil
	}
	encoded, _, err := transform.Bytes(encoding.Codec.NewEncoder(), []byte(content))
	if err != nil {
		return nil, encoding.Label, fmt.Errorf("encode %s: %w", encoding.Label, err)
	}
	return encoded, encoding.Label, nil
}

func lookupFileTextEncoding(name string) (fileTextEncoding, bool) {
	normalized := normalizeEncodingName(name)
	if normalized == "" || normalized == "utf8" || normalized == "utf-8" || normalized == "cp65001" || normalized == "65001" {
		return fileTextEncoding{Label: "utf-8"}, true
	}
	switch normalized {
	case "gbk", "gb18030", "gb2312", "cp936", "windows936", "936", "cp54936", "54936":
		return fileTextEncoding{Label: "gb18030", Codec: simplifiedchinese.GB18030}, true
	case "big5", "big5hkscs", "cp950", "windows950", "950":
		return fileTextEncoding{Label: "big5", Codec: traditionalchinese.Big5}, true
	case "shiftjis", "shiftjisx0213", "sjis", "mskanji", "cp932", "windows932", "932":
		return fileTextEncoding{Label: "shift_jis", Codec: japanese.ShiftJIS}, true
	case "euckr", "ksc5601", "ksc56011987", "cp949", "windows949", "949":
		return fileTextEncoding{Label: "euc-kr", Codec: korean.EUCKR}, true
	case "cp437", "ibm437", "oem437", "437":
		return fileTextEncoding{Label: "cp437", Codec: charmap.CodePage437}, true
	case "cp850", "ibm850", "oem850", "850":
		return fileTextEncoding{Label: "cp850", Codec: charmap.CodePage850}, true
	case "cp852", "ibm852", "oem852", "852":
		return fileTextEncoding{Label: "cp852", Codec: charmap.CodePage852}, true
	case "cp855", "ibm855", "oem855", "855":
		return fileTextEncoding{Label: "cp855", Codec: charmap.CodePage855}, true
	case "cp858", "ibm858", "oem858", "858":
		return fileTextEncoding{Label: "cp858", Codec: charmap.CodePage858}, true
	case "cp866", "ibm866", "oem866", "866":
		return fileTextEncoding{Label: "cp866", Codec: charmap.CodePage866}, true
	case "windows1250", "cp1250", "1250":
		return fileTextEncoding{Label: "windows-1250", Codec: charmap.Windows1250}, true
	case "windows1251", "cp1251", "1251":
		return fileTextEncoding{Label: "windows-1251", Codec: charmap.Windows1251}, true
	case "windows1252", "cp1252", "1252":
		return fileTextEncoding{Label: "windows-1252", Codec: charmap.Windows1252}, true
	case "windows1253", "cp1253", "1253":
		return fileTextEncoding{Label: "windows-1253", Codec: charmap.Windows1253}, true
	case "windows1254", "cp1254", "1254":
		return fileTextEncoding{Label: "windows-1254", Codec: charmap.Windows1254}, true
	case "windows1255", "cp1255", "1255":
		return fileTextEncoding{Label: "windows-1255", Codec: charmap.Windows1255}, true
	case "windows1256", "cp1256", "1256":
		return fileTextEncoding{Label: "windows-1256", Codec: charmap.Windows1256}, true
	case "windows1257", "cp1257", "1257":
		return fileTextEncoding{Label: "windows-1257", Codec: charmap.Windows1257}, true
	case "windows1258", "cp1258", "1258":
		return fileTextEncoding{Label: "windows-1258", Codec: charmap.Windows1258}, true
	default:
		return fileTextEncoding{}, false
	}
}

func normalizeEncodingName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

func fileTextEncodingCandidates() []fileTextEncoding {
	candidates := make([]fileTextEncoding, 0, 5)
	if runtime.GOOS == "windows" {
		if encoding, ok := lookupFileTextEncoding(fmt.Sprintf("cp%d", subprocessOutputCodePage())); ok && encoding.Codec != nil {
			candidates = append(candidates, encoding)
		}
	}
	for _, name := range []string{
		"gb18030",
		"big5",
		"shift_jis",
		"euc-kr",
	} {
		encoding, ok := lookupFileTextEncoding(name)
		if !ok || duplicateFileTextEncoding(candidates, encoding.Label) {
			continue
		}
		candidates = append(candidates, encoding)
	}
	return candidates
}

func duplicateFileTextEncoding(values []fileTextEncoding, label string) bool {
	for _, value := range values {
		if value.Label == label {
			return true
		}
	}
	return false
}

func decodeBytesWithFileEncoding(data []byte, encoding fileTextEncoding) (string, error) {
	if encoding.Label == "utf-8" {
		if !utf8.Valid(data) {
			return "", fmt.Errorf("content is not valid UTF-8")
		}
		return string(data), nil
	}
	decoded, _, err := transform.Bytes(encoding.Codec.NewDecoder(), data)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", encoding.Label, err)
	}
	return string(decoded), nil
}

func looksLikeTextBytes(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	control := 0
	for _, b := range data {
		if b == 0 {
			return false
		}
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
			control++
		}
	}
	return control*100 <= len(data)*5
}

func looksLikeDecodedText(content string) bool {
	if strings.ContainsRune(content, utf8.RuneError) {
		return false
	}
	if content == "" {
		return true
	}
	control := 0
	total := 0
	for _, r := range content {
		total++
		if r == 0 {
			return false
		}
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != '\f' {
			control++
		}
	}
	return total == 0 || control*100 <= total*5
}

func decodedTextScore(content string) int {
	score := 0
	for _, r := range content {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			score++
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			score += 6
		case r > 127 && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)):
			score += 3
		case r >= 0x20 && r < 0x7f:
			score += 2
		default:
			score--
		}
	}
	return score
}
