package connectorauth

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"agent-platform/internal/observability"
)

var (
	diagnosticUserInfo  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+@`)
	diagnosticBasicAuth = regexp.MustCompile(`(?i)(\bBasic\s+)[a-z0-9+/=]+`)
	diagnosticNPMAuth   = regexp.MustCompile(`(?i)((?:_auth(?:token)?|_password)\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^']*'|[^\s,;&}\]<>]+)`)
	npmExactVersion     = regexp.MustCompile(`.@v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?$`)
	shellSeparators     = regexp.MustCompile(`&&|\|\||[;|\n]`)
)

// diagnosticExcerpt keeps the end of init/versionCheck output, where npm
// reports the failure, with credentials, URL userinfo and control bytes removed.
// It is returned by the preparation API and persisted with the attempt.
func diagnosticExcerpt(output string) string {
	output = diagnosticBasicAuth.ReplaceAllString(output, "${1}"+observability.HiddenToken)
	output = diagnosticNPMAuth.ReplaceAllString(output, "${1}"+observability.HiddenToken)
	output = observability.SanitizeLog(output)
	output = diagnosticUserInfo.ReplaceAllString(output, "${1}"+observability.HiddenToken+"@")
	output = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(output))
	if chars := []rune(output); len(chars) > 4096 {
		output = "[truncated]\n" + string(chars[len(chars)-4096:])
	}
	return output
}

// unpinnedNPMInit reports an init that installs an npm package without an
// exact version. Such a package still installs; preparing again may change it.
func unpinnedNPMInit(cli map[string]any) bool {
	line, err := cliLine(cli["init"], false)
	if err != nil {
		return false
	}
	for _, segment := range shellSeparators.Split(line, -1) {
		words := strings.Fields(segment)
		if len(words) < 2 || strings.TrimSuffix(strings.ToLower(filepath.Base(words[0])), ".cmd") != "npm" {
			continue
		}
		install := -1
		for i, word := range words[1:] {
			if word == "install" || word == "i" || word == "add" {
				install = i + 2
				break
			}
		}
		if install < 0 {
			continue
		}
		for i := install; i < len(words); i++ {
			word := strings.Trim(words[i], `"'`)
			if strings.HasPrefix(word, "-") {
				if !strings.Contains(word, "=") && (word == "--registry" || word == "--cache" || word == "--loglevel" || word == "--prefix") {
					i++
				}
				continue
			}
			if !npmExactVersion.MatchString(word) {
				return true
			}
		}
	}
	return false
}
