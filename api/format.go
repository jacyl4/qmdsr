package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"qmdsr/model"
)

func renderFormattedText(results []model.SearchResult, meta model.SearchMeta, filesOnly bool) string {
	var b strings.Builder
	scope := formatScope(meta.CollectionsSearched)
	if filesOnly {
		fmt.Fprintf(&b, "## 相关文件 (%d hits)\n\n", len(results))
		for _, r := range results {
			fmt.Fprintf(&b, "%s (%.2f)\n", preferredHitURI(r), r.Score)
		}
		return strings.TrimSpace(b.String())
	}

	fmt.Fprintf(&b, "## 检索结果 (%s, %d hits)\n\n", scope, len(results))
	for i, r := range results {
		fmt.Fprintf(&b, "%d. [%.2f] %s\n", i+1, r.Score, preferredHitURI(r))
		snippet := strings.TrimSpace(r.Snippet)
		if snippet == "" {
			b.WriteString("\n")
			continue
		}
		for _, line := range strings.Split(snippet, "\n") {
			b.WriteString("   ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func renderSearchAndGetText(fileHits []model.SearchResult, docs []model.Document, truncated []string, meta model.SearchMeta) string {
	var b strings.Builder
	scope := formatScope(meta.CollectionsSearched)
	fmt.Fprintf(&b, "## 检索命中 (%s, %d files)\n\n", scope, len(fileHits))

	for i, doc := range docs {
		score := findScoreByURI(fileHits, doc.File)
		fmt.Fprintf(&b, "### 精读 %d/%d: %s (score: %.2f)\n\n", i+1, len(docs), doc.File, score)
		b.WriteString(preserveStructuredBlock(doc.Content))
		b.WriteString("\n\n")
	}

	if len(truncated) > 0 {
		b.WriteString("### 已省略内容\n\n")
		for _, file := range truncated {
			b.WriteString(file)
			b.WriteString(" (TRUNCATED)\n")
		}
		b.WriteString("\n")
	}

	if len(fileHits) > len(docs) {
		b.WriteString("### 其他相关文件\n\n")
		docSet := make(map[string]struct{}, len(docs))
		for _, doc := range docs {
			docSet[doc.File] = struct{}{}
		}
		for _, hit := range fileHits {
			uri := preferredHitURI(hit)
			if _, ok := docSet[uri]; ok {
				continue
			}
			fmt.Fprintf(&b, "%s (%.2f)\n", uri, hit.Score)
		}
	}

	return strings.TrimSpace(b.String())
}

func formatScope(collections []string) string {
	if len(collections) == 0 {
		return "all"
	}
	return strings.Join(collections, ", ")
}

func preferredHitURI(r model.SearchResult) string {
	if strings.TrimSpace(r.File) != "" {
		return r.File
	}
	return r.Title
}

func findScoreByURI(hits []model.SearchResult, uri string) float64 {
	for _, h := range hits {
		if preferredHitURI(h) == uri {
			return h.Score
		}
	}
	return 0
}

func preserveStructuredBlock(content string) string {
	if content == "" {
		return ""
	}

	trimmed := strings.TrimSpace(content)
	if language := detectStructuredLanguage(trimmed); language != "" {
		return fenceBlock(content, language)
	}
	return content
}

func detectStructuredLanguage(content string) string {
	if json.Valid([]byte(content)) {
		return "json"
	}
	return ""
}

func fenceBlock(content string, language string) string {
	fence := "```"
	if strings.Contains(content, "```") {
		fence = "````"
	}
	if language == "" {
		return fence + "\n" + content + "\n" + fence
	}
	return fence + language + "\n" + content + "\n" + fence
}
