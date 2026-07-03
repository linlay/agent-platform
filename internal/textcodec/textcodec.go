package textcodec

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/runtimeenv"

	textencoding "golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

type DecodedText struct {
	Content  string
	Encoding string
}

type Encoding struct {
	Label string
	codec textencoding.Encoding
}

func DecodeFileText(data []byte, requestedEncoding string, env runtimeenv.Info) (DecodedText, bool, error) {
	if strings.TrimSpace(requestedEncoding) != "" {
		encoding, ok := LookupFileEncoding(requestedEncoding)
		if !ok {
			return DecodedText{}, false, fmt.Errorf("unsupported encoding: %s", requestedEncoding)
		}
		text, err := decodeBytes(data, encoding)
		if err != nil {
			return DecodedText{}, false, err
		}
		if !LooksLikeDecodedText(text) {
			return DecodedText{}, false, fmt.Errorf("decoded content is not text")
		}
		return DecodedText{Content: text, Encoding: encoding.Label}, true, nil
	}
	if utf8.Valid(data) {
		return DecodedText{Content: string(data), Encoding: "utf-8"}, true, nil
	}
	if !LooksLikeTextBytes(data) {
		return DecodedText{}, false, nil
	}
	for _, encoding := range defaultFileEncodingCandidates(env) {
		text, err := decodeBytes(data, encoding)
		if err != nil || !LooksLikeDecodedText(text) {
			continue
		}
		return DecodedText{Content: text, Encoding: encoding.Label}, true, nil
	}
	return DecodedText{}, false, nil
}

func EncodeFileText(content string, encodingName string) ([]byte, string, error) {
	encoding, ok := LookupFileEncoding(encodingName)
	if !ok {
		return nil, "", fmt.Errorf("unsupported encoding: %s", encodingName)
	}
	if encoding.Label == "utf-8" {
		return []byte(content), encoding.Label, nil
	}
	encoded, _, err := transform.Bytes(encoding.codec.NewEncoder(), []byte(content))
	if err != nil {
		return nil, encoding.Label, fmt.Errorf("encode %s: %w", encoding.Label, err)
	}
	return encoded, encoding.Label, nil
}

func DecodeSubprocessOutput(output []byte, env runtimeenv.Info) string {
	if len(output) == 0 {
		return ""
	}
	if utf8.Valid(output) {
		return string(output)
	}
	if env.GOOS == "windows" && env.ACP > 0 {
		if encoding, ok := encodingForCodePage(env.ACP); ok && encoding.codec != nil {
			decoded, err := decodeBytes(output, encoding)
			if err == nil {
				return strings.ToValidUTF8(decoded, "\uFFFD")
			}
		}
	}
	return strings.ToValidUTF8(string(output), "\uFFFD")
}

func LookupFileEncoding(name string) (Encoding, bool) {
	normalized := NormalizeEncodingName(name)
	if normalized == "" || normalized == "utf8" || normalized == "utf-8" || normalized == "cp65001" || normalized == "65001" {
		return Encoding{Label: "utf-8"}, true
	}
	switch normalized {
	case "gbk", "gb18030", "gb2312", "cp936", "windows936", "936", "cp54936", "54936":
		return Encoding{Label: "gb18030", codec: simplifiedchinese.GB18030}, true
	default:
		return Encoding{}, false
	}
}

func LookupEncoding(name string) (Encoding, bool) {
	normalized := NormalizeEncodingName(name)
	if normalized == "" || normalized == "utf8" || normalized == "utf-8" || normalized == "cp65001" || normalized == "65001" {
		return Encoding{Label: "utf-8"}, true
	}
	switch normalized {
	case "gbk", "gb18030", "gb2312", "cp936", "windows936", "936", "cp54936", "54936":
		return Encoding{Label: "gb18030", codec: simplifiedchinese.GB18030}, true
	case "big5", "big5hkscs", "cp950", "windows950", "950":
		return Encoding{Label: "big5", codec: traditionalchinese.Big5}, true
	case "shiftjis", "shiftjisx0213", "sjis", "mskanji", "cp932", "windows932", "932":
		return Encoding{Label: "shift_jis", codec: japanese.ShiftJIS}, true
	case "euckr", "ksc5601", "ksc56011987", "cp949", "windows949", "949":
		return Encoding{Label: "euc-kr", codec: korean.EUCKR}, true
	case "cp437", "ibm437", "oem437", "437":
		return Encoding{Label: "cp437", codec: charmap.CodePage437}, true
	case "cp850", "ibm850", "oem850", "850":
		return Encoding{Label: "cp850", codec: charmap.CodePage850}, true
	case "cp852", "ibm852", "oem852", "852":
		return Encoding{Label: "cp852", codec: charmap.CodePage852}, true
	case "cp855", "ibm855", "oem855", "855":
		return Encoding{Label: "cp855", codec: charmap.CodePage855}, true
	case "cp858", "ibm858", "oem858", "858":
		return Encoding{Label: "cp858", codec: charmap.CodePage858}, true
	case "cp866", "ibm866", "oem866", "866":
		return Encoding{Label: "cp866", codec: charmap.CodePage866}, true
	case "windows1250", "cp1250", "1250":
		return Encoding{Label: "windows-1250", codec: charmap.Windows1250}, true
	case "windows1251", "cp1251", "1251":
		return Encoding{Label: "windows-1251", codec: charmap.Windows1251}, true
	case "windows1252", "cp1252", "1252":
		return Encoding{Label: "windows-1252", codec: charmap.Windows1252}, true
	case "windows1253", "cp1253", "1253":
		return Encoding{Label: "windows-1253", codec: charmap.Windows1253}, true
	case "windows1254", "cp1254", "1254":
		return Encoding{Label: "windows-1254", codec: charmap.Windows1254}, true
	case "windows1255", "cp1255", "1255":
		return Encoding{Label: "windows-1255", codec: charmap.Windows1255}, true
	case "windows1256", "cp1256", "1256":
		return Encoding{Label: "windows-1256", codec: charmap.Windows1256}, true
	case "windows1257", "cp1257", "1257":
		return Encoding{Label: "windows-1257", codec: charmap.Windows1257}, true
	case "windows1258", "cp1258", "1258":
		return Encoding{Label: "windows-1258", codec: charmap.Windows1258}, true
	default:
		return Encoding{}, false
	}
}

func NormalizeEncodingName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

func LooksLikeTextBytes(data []byte) bool {
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

func LooksLikeDecodedText(content string) bool {
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

func defaultFileEncodingCandidates(env runtimeenv.Info) []Encoding {
	candidates := make([]Encoding, 0, 2)
	if env.GOOS == "windows" && env.ACP > 0 {
		if encoding, ok := encodingForCodePage(env.ACP); ok && encoding.codec != nil {
			candidates = append(candidates, encoding)
		}
	}
	if encoding, ok := LookupEncoding("gb18030"); ok && !duplicateEncoding(candidates, encoding.Label) {
		candidates = append(candidates, encoding)
	}
	return candidates
}

func encodingForCodePage(codePage uint32) (Encoding, bool) {
	if codePage == 0 {
		return Encoding{}, false
	}
	return LookupEncoding(fmt.Sprintf("cp%d", codePage))
}

func duplicateEncoding(values []Encoding, label string) bool {
	for _, value := range values {
		if value.Label == label {
			return true
		}
	}
	return false
}

func decodeBytes(data []byte, encoding Encoding) (string, error) {
	if encoding.Label == "utf-8" {
		if !utf8.Valid(data) {
			return "", fmt.Errorf("content is not valid UTF-8")
		}
		return string(data), nil
	}
	decoded, _, err := transform.Bytes(encoding.codec.NewDecoder(), data)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", encoding.Label, err)
	}
	return string(decoded), nil
}
