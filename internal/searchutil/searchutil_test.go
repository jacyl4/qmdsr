package searchutil

import (
	"testing"

	"qmdsr/model"
)

func TestDedupSortLimit_EnforcesFileDiversity(t *testing.T) {
	in := []model.SearchResult{
		{DocID: "a1", File: "a.md", Score: 0.95},
		{DocID: "a2", File: "a.md", Score: 0.90},
		{DocID: "a3", File: "a.md", Score: 0.85},
		{DocID: "b1", File: "b.md", Score: 0.80},
		{DocID: "c1", File: "c.md", Score: 0.70},
	}

	out := DedupSortLimit(in, 4)
	if len(out) != 4 {
		t.Fatalf("expected 4 results, got %d", len(out))
	}

	countA := 0
	for _, r := range out {
		if r.File == "a.md" {
			countA++
		}
	}
	if countA != 2 {
		t.Fatalf("expected 2 results from a.md, got %d", countA)
	}
}

func TestDedupSortLimit_StillDedupsWhenTopKUnlimited(t *testing.T) {
	in := []model.SearchResult{
		{DocID: "dup", File: "a.md", Score: 0.9},
		{DocID: "dup", File: "a.md", Score: 0.8},
		{DocID: "b1", File: "b.md", Score: 0.7},
	}

	out := DedupSortLimit(in, 0)
	if len(out) != 2 {
		t.Fatalf("expected 2 deduped results, got %d", len(out))
	}
}
