package textutil

import (
	"strings"
	"unicode"
)

// IsCJK reports whether r is within common Han ranges.
func IsCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0xF900 && r <= 0xFAFF)
}

// CountCJK counts CJK runes in a string.
func CountCJK(s string) int {
	n := 0
	for _, r := range s {
		if IsCJK(r) {
			n++
		}
	}
	return n
}

// CountWordsMixed counts words with CJK-aware splitting.
func CountWordsMixed(s string) int {
	count := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if inWord {
				count++
				inWord = false
			}
			continue
		}
		if !inWord {
			inWord = true
		}
		if IsCJK(r) {
			if inWord {
				count++
				inWord = false
			}
			count++
		}
	}
	if inWord {
		count++
	}
	return count
}

// CountWordsMaxFieldsOrCJK preserves the previous deep-routing heuristic.
func CountWordsMaxFieldsOrCJK(s string) int {
	asciiWords := len(strings.Fields(s))
	cjkWords := CountCJK(s)
	if cjkWords > asciiWords {
		return cjkWords
	}
	return asciiWords
}
