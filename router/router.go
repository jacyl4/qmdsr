package router

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"qmdsr/internal/textutil"
)

type Mode string

const (
	ModeSearch  Mode = "search"
	ModeVSearch Mode = "vsearch"
	ModeQuery   Mode = "query"
	ModeAuto    Mode = "auto"
)

var (
	quotedPattern  = regexp.MustCompile(`"[^"]+"`)
	questionPrefix = []string{
		"如何", "怎么", "怎样", "什么", "为什么", "为何",
		"哪些", "哪个", "哪里", "谁", "多少", "是否",
		"能不能", "可以", "应该",
	}
	temporalWords = []string{
		"之前", "上次", "昨天", "今天", "最近", "过去",
		"以前", "历史", "曾经", "earlier", "previous", "last time",
	}
)

func DetectMode(query string, hasVector bool, hasDeepQuery bool) Mode {
	query = strings.TrimSpace(query)
	if query == "" {
		return ModeSearch
	}

	if quotedPattern.MatchString(query) {
		return ModeSearch
	}

	words := textutil.CountWordsMixed(query)
	if words <= 3 && isPredominantlyASCII(query) {
		return ModeSearch
	}

	if hasDeepQuery {
		for _, prefix := range questionPrefix {
			if strings.HasPrefix(query, prefix) {
				return ModeQuery
			}
		}
		for _, word := range temporalWords {
			if strings.Contains(query, word) {
				return ModeQuery
			}
		}
		if words > 8 {
			return ModeQuery
		}
	}

	if hasVector && words >= 4 {
		return ModeVSearch
	}
	if hasVector && !isPredominantlyASCII(query) && words >= 2 {
		return ModeVSearch
	}

	if words <= 3 && isPredominantlyASCII(query) {
		return ModeSearch
	}

	return ModeSearch
}

func isPredominantlyASCII(s string) bool {
	ascii := 0
	total := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			total++
			if r < 128 {
				ascii++
			}
		}
		i += size
	}
	if total == 0 {
		return true
	}
	return float64(ascii)/float64(total) > 0.8
}
