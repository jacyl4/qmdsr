package api

import (
	"strings"
	"testing"

	"qmdsr/model"
)

func TestPreserveStructuredBlock_JSONKeepsRawContent(t *testing.T) {
	raw := "{\n  \"service\": \"gateway\",\n  \"burst\": 50\n}\n"
	out := preserveStructuredBlock(raw)

	if !strings.Contains(out, "```json") {
		t.Fatalf("expected json fenced block, got: %q", out)
	}
	if !strings.Contains(out, raw) {
		t.Fatalf("expected raw json content to be preserved")
	}
}

func TestRenderSearchAndGetText_WithTruncatedMarker(t *testing.T) {
	hits := []model.SearchResult{
		{File: "a.md", Score: 0.9},
		{File: "b.md", Score: 0.8},
	}
	docs := []model.Document{
		{File: "a.md", Content: "plain text"},
	}
	out := renderSearchAndGetText(hits, docs, []string{"b.md"}, model.SearchMeta{
		CollectionsSearched: []string{"claw-memory"},
	})

	if !strings.Contains(out, "b.md (TRUNCATED)") {
		t.Fatalf("expected truncated marker in formatted text, got: %q", out)
	}
	if !strings.Contains(out, "### 其他相关文件") {
		t.Fatalf("expected remaining file section in formatted text, got: %q", out)
	}
}
