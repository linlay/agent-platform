package commands

import (
	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

var rgSpec = types.OptionSpec{ShortFlags: "abcFhHiIlLnNopqsSuUvwxz0", ShortArgs: map[byte]types.OptionRole{'e': types.OptionValue, 'f': types.OptionRead, 'g': types.OptionValue, 't': types.OptionValue, 'T': types.OptionValue, 'm': types.OptionValue, 'A': types.OptionValue, 'B': types.OptionValue, 'C': types.OptionValue, 'j': types.OptionValue, 'M': types.OptionValue, 'r': types.OptionValue, 'E': types.OptionValue, 'd': types.OptionValue},
	Long: options.Merge(options.Longs(types.OptionNone, "hidden", "no-ignore", "no-ignore-vcs", "smart-case", "case-sensitive", "ignore-case", "json", "vimgrep", "heading", "no-heading", "follow", "fixed-strings", "multiline", "pcre2", "stats", "files", "files-with-matches", "files-without-match", "count", "count-matches", "line-number", "no-line-number", "only-matching", "invert-match", "word-regexp", "line-regexp", "search-zip", "text", "null", "no-filename", "with-filename", "no-messages", "quiet", "trim", "type-list", "no-config", "one-file-system", "unrestricted", "binary", "column", "byte-offset", "passthru", "max-columns-preview"), options.Longs(types.OptionValue, "glob", "iglob", "type", "type-not", "type-add", "sort", "sortr", "max-count", "max-depth", "max-filesize", "threads", "after-context", "before-context", "context", "regexp", "replace", "encoding", "max-columns", "path-separator", "colors", "color", "pre-glob", "field-match-separator", "field-context-separator", "context-separator"), options.Longs(types.OptionRead, "file", "ignore-file"), options.Longs(types.OptionExec, "pre"))}

func rgConfig(args []string) []string {
	if !configDisabled(args) {
		return []string{"RIPGREP_CONFIG_PATH"}
	}
	return nil
}
