package textutil

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reHeading      = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reLink         = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reBoldStar     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalicStar   = regexp.MustCompile(`\*([^*\n]+)\*`)
	reBoldUnders   = regexp.MustCompile(`__([^_\n]+)__`)
	reItalicUnders = regexp.MustCompile(`_([^_\n]+)_`)
	reBlankLines   = regexp.MustCompile(`\n{3,}`)
)

// CleanSnippet removes markdown-heavy noise and applies a rune-safe max length.
func CleanSnippet(s string, maxLen int) string {
	s = reHeading.ReplaceAllString(s, "")
	s = reLink.ReplaceAllString(s, "$1")
	s = reBoldStar.ReplaceAllString(s, "$1")
	s = reItalicStar.ReplaceAllString(s, "$1")
	s = reBoldUnders.ReplaceAllString(s, "$1")
	s = reItalicUnders.ReplaceAllString(s, "$1")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if maxLen > 0 && utf8.RuneCountInString(s) > maxLen {
		rs := []rune(s)
		if maxLen <= 3 {
			s = string(rs[:maxLen])
		} else {
			cut := maxLen - 3
			// Try to keep sentence boundaries when truncating snippets.
			start := cut - 200
			if start < 0 {
				start = 0
			}
			for i := cut - 1; i >= start; i-- {
				if isSentenceEnd(rs[i]) {
					cut = i + 1
					break
				}
			}
			if cut < 0 {
				cut = 0
			}
			s = strings.TrimSpace(string(rs[:cut])) + "..."
		}
	}
	return s
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '.', '？', '?', '！', '!', '\n':
		return true
	default:
		return false
	}
}
