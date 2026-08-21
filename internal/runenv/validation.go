package runenv

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var portableNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

var hardDeniedNames = map[string]struct{}{
	"AP_AGENT_CONFIG_HOME": {}, "AP_WORKSPACE_DIR": {}, "AP_CHAT_DIR": {}, "AP_ACCESS_TOKEN": {},
	"PATH": {}, "PATHEXT": {}, "COMSPEC": {}, "SYSTEMROOT": {}, "WINDIR": {},
	"ENV": {}, "BASH_ENV": {}, "ZDOTDIR": {}, "SHELLOPTS": {}, "CDPATH": {}, "GLOBIGNORE": {},
	"INPUTRC": {}, "HISTFILE": {}, "PS1": {}, "PS2": {}, "PS3": {}, "PS4": {}, "PROMPT": {}, "PROMPT_COMMAND": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "LD_AUDIT": {}, "LD_DEBUG": {},
	"DYLD_INSERT_LIBRARIES": {}, "DYLD_LIBRARY_PATH": {}, "DYLD_FRAMEWORK_PATH": {},
	"NODE_OPTIONS": {}, "PYTHONPATH": {}, "PYTHONHOME": {}, "RUBYOPT": {}, "PERL5OPT": {},
	"GIT_SSH_COMMAND": {}, "SSH_ASKPASS": {},
}

func NormalizeName(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

func ValidateName(name string, extraDenied []string) error {
	name = strings.TrimSpace(name)
	if !portableNamePattern.MatchString(name) {
		return fmt.Errorf("environment variable name must match %s", portableNamePattern.String())
	}
	upper := strings.ToUpper(name)
	if _, denied := hardDeniedNames[upper]; denied {
		return fmt.Errorf("environment variable %s is reserved or denied", name)
	}
	for _, item := range extraDenied {
		if strings.EqualFold(strings.TrimSpace(item), name) {
			return fmt.Errorf("environment variable %s is denied by platform policy", name)
		}
	}
	return nil
}

func ValidateValue(value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("value must be valid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("value must not contain NUL")
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("value exceeds %d bytes", maxBytes)
	}
	return nil
}

func HardDeniedNames() []string {
	out := make([]string, 0, len(hardDeniedNames))
	for name := range hardDeniedNames {
		out = append(out, name)
	}
	return out
}
