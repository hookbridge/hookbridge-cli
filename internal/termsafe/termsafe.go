// Package termsafe sanitises server-supplied text before it reaches a terminal.
package termsafe

import (
	"fmt"
	"strings"
	"unicode"
)

// Clean drops control characters (C0, DEL and C1) and turns newline and tab into spaces.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// EscapeC1 replaces C1 control characters with their \u00XX JSON escapes, which
// keeps pretty-printed JSON valid while stopping them reaching the terminal raw.
func EscapeC1(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
