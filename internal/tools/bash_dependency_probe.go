package tools

import (
	"agent-platform/internal/shellreview/types"
	"encoding/hex"
	"fmt"
	"strings"
)

// Immutable entry checks precede every utility invocation. This profile is
// intentionally limited to real GNU-compatible entries in /usr/bin.
func immutableDependencyTools(programs ...string) string {
	guards := []string{"test ! -L /usr/bin", "test ! -w /usr/bin", "test ! -L /usr", "test ! -w /usr", "test ! -w /"}
	for _, program := range programs {
		path := "/usr/bin/" + program
		guards = append(guards, "test -x "+path, "test ! -L "+path, "test ! -w "+path)
	}
	return strings.Join(guards, " && ") + " && "
}
func sandboxConfigProbeCommand(target string) string {
	// Paths are single-quoted data. The protocol uses NUL separators so spaces,
	// newlines and pipes in filenames never become parser structure or commands.
	return immutableDependencyTools("sha256sum", "readlink", "stat") + "{ target=" + shellQuoteProbe(target) + `; if test -e "$target" || test -L "$target"; then
 test -f "$target" && test -r "$target" || exit 1
 before=$(/usr/bin/stat -Lc '%s|%Y|%i' -- "$target") || exit 1
 size=${before%%|*}; case "$size" in ''|*[!0-9]*) exit 1;; esac
 test "$size" -le 8388608 || exit 1
 digest=$(/usr/bin/sha256sum < "$target") || exit 1
 digest=${digest%% *}
 after=$(/usr/bin/stat -Lc '%s|%Y|%i' -- "$target") || exit 1
 test "$before" = "$after" || exit 1
 printf 'file\0'
 /usr/bin/readlink -z -f -- "$target" || exit 1
 printf '%s\0' "$digest"
else
 parent="$target"
 while :; do
  parent=${parent%/*}; test -n "$parent" || parent=/
  if test -e "$parent" || test -L "$parent"; then test -d "$parent" && test -x "$parent" || exit 1; printf 'missing\0'; /usr/bin/readlink -m -z -- "$target" || exit 1; break; fi
  test "$parent" != / || exit 1
 done
fi; }`
}
func parseConfigProbe(raw, target string) (types.FileInspection, error) {
	if strings.HasPrefix(raw, "missing\x00") {
		fields := strings.Split(raw, "\x00")
		if len(fields) != 3 || !strings.HasPrefix(fields[1], "/") || fields[2] != "" {
			return types.FileInspection{}, fmt.Errorf("container missing-path response invalid")
		}
		return types.FileInspection{Canonical: fields[1], Alias: fields[1] != target}, nil
	}
	fields := strings.Split(raw, "\x00")
	if len(fields) != 4 || fields[0] != "file" || fields[3] != "" || !strings.HasPrefix(fields[1], "/") || len(fields[2]) != 64 {
		return types.FileInspection{}, fmt.Errorf("container config response invalid")
	}
	if _, err := hex.DecodeString(fields[2]); err != nil {
		return types.FileInspection{}, fmt.Errorf("container config digest invalid")
	}
	return types.FileInspection{Exists: true, Canonical: fields[1], SHA256: fields[2], Alias: fields[1] != target}, nil
}

const accountHomeProbeBody = `test -n "$UID" && test -n "$EUID" && test "$UID" = "$EUID" || exit 1
record=$(/usr/bin/getent passwd "$UID"); status=$?
if test "$status" = 2; then printf 'none\0'; exit 0; fi
test "$status" = 0 || exit 1
printf 'account\0%s\0' "$record"`

func parseAccountHomeProbe(raw string) (string, error) {
	if raw == "none\x00" {
		return "", nil
	}
	fields := strings.Split(raw, "\x00")
	if len(fields) != 3 || fields[0] != "account" || fields[2] != "" {
		return "", fmt.Errorf("container account response invalid")
	}
	entry := strings.Split(fields[1], ":")
	if len(entry) != 7 || !strings.HasPrefix(entry[5], "/") {
		return "", fmt.Errorf("container account home invalid")
	}
	return entry[5], nil
}
