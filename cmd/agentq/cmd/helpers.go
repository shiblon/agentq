package cmd

import "strings"

// truncate returns s truncated to at most n runes with "..." appended if cut.
// Newlines are replaced with spaces so the result is safe for single-line display.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
