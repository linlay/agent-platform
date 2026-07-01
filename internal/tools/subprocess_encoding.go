package tools

import (
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func decodeSubprocessOutput(output []byte) string {
	return decodeSubprocessOutputBytes(output, subprocessOutputCodePage())
}

func decodeSubprocessOutputBytes(output []byte, codePage uint32) string {
	if len(output) == 0 {
		return ""
	}
	if utf8.Valid(output) {
		return string(output)
	}
	if decoder := subprocessOutputDecoder(codePage); decoder != nil {
		decoded, err := decoder.Bytes(output)
		if err == nil {
			return strings.ToValidUTF8(string(decoded), "\uFFFD")
		}
	}
	return strings.ToValidUTF8(string(output), "\uFFFD")
}

func subprocessOutputDecoder(codePage uint32) *encoding.Decoder {
	switch codePage {
	case 936, 54936:
		return simplifiedchinese.GB18030.NewDecoder()
	case 950:
		return traditionalchinese.Big5.NewDecoder()
	default:
		return nil
	}
}

func windowsShellUTF8Command(shellExecutable string, command string, goos string) string {
	if goos != "windows" {
		return command
	}
	switch normalizedShellBase(shellExecutable) {
	case "powershell", "pwsh":
		return "$OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false; [Console]::OutputEncoding = $OutputEncoding; " + command
	case "cmd":
		return "chcp 65001 >NUL & " + command
	default:
		return command
	}
}

func currentGOOS() string {
	return runtime.GOOS
}
