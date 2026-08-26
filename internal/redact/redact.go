package redact

import "strings"

func URL(raw string) string {
	if raw == "" {
		return ""
	}

	lastAt := strings.LastIndex(raw, "@")
	if lastAt == -1 {
		return raw
	}

	start := 0
	if colon := strings.Index(raw, ":"); colon > 0 && colon < lastAt &&
		isValidScheme(raw[:colon]) && strings.HasPrefix(raw[colon+1:], "//") {
		start = colon + len("://")
	}

	return raw[:start] + "REDACTED" + raw[lastAt:]
}

func isValidScheme(s string) bool {
	if s == "" || !isASCIILetter(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isASCIILetter(c) && !isASCIIDigit(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
