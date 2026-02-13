package executor

import (
	"strings"
	"testing"
)

func TestParseCollectionListJSON_Array(t *testing.T) {
	out := `[{"name":"alpha","path":"/tmp/a","mask":"**/*.md","files":12}]`
	cols, err := parseCollectionListJSON(out)
	if err != nil {
		t.Fatalf("parseCollectionListJSON failed: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "alpha" || cols[0].Files != 12 {
		t.Fatalf("unexpected parse result: %+v", cols)
	}
}

func TestParseCollectionListJSON_Wrapped(t *testing.T) {
	out := `{"collections":[{"name":"beta","mask":"*.txt","files":3}]}`
	cols, err := parseCollectionListJSON(out)
	if err != nil {
		t.Fatalf("parseCollectionListJSON failed: %v", err)
	}
	if len(cols) != 1 || cols[0].Name != "beta" || cols[0].Mask != "*.txt" {
		t.Fatalf("unexpected parse result: %+v", cols)
	}
}

func TestParseCollectionListText(t *testing.T) {
	out := `Collections (2):

alpha (qmd://alpha/)
  Pattern:  **/*.md
  Files:    12
  Updated:  1h ago

beta (qmd://beta/)
  Pattern:  docs/*.md
  Files:    7
`

	cols, err := parseCollectionListText(out)
	if err != nil {
		t.Fatalf("parseCollectionListText failed: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("unexpected count: %d", len(cols))
	}
	if cols[0].Name != "alpha" || cols[0].Mask != "**/*.md" || cols[0].Files != 12 {
		t.Fatalf("unexpected first collection: %+v", cols[0])
	}
	if cols[1].Name != "beta" || cols[1].Mask != "docs/*.md" || cols[1].Files != 7 {
		t.Fatalf("unexpected second collection: %+v", cols[1])
	}
}

func TestParseStatusJSON(t *testing.T) {
	out := `{"vectors":32,"collections":[{"name":"alpha","files":12}]}`
	st, err := parseStatusJSON(out)
	if err != nil {
		t.Fatalf("parseStatusJSON failed: %v", err)
	}
	if st.Vectors != 32 {
		t.Fatalf("unexpected vectors: %d", st.Vectors)
	}
}

func TestParseStatusText(t *testing.T) {
	out := `QMD Status

Documents
  Total:    581 files indexed
  Vectors:  32 embedded
  Pending:  534 need embedding

Collections
  alpha (qmd://alpha/)
    Files:    28 (updated 1h ago)
  beta (qmd://beta/)
    Files:    67 (updated 3h ago)
`

	st, err := parseStatusText(out)
	if err != nil {
		t.Fatalf("parseStatusText failed: %v", err)
	}
	if st.Vectors != 32 {
		t.Fatalf("unexpected vectors: %d", st.Vectors)
	}
	if len(st.Collections) != 2 {
		t.Fatalf("unexpected collection count: %d", len(st.Collections))
	}
	if st.Collections[0].Name != "alpha" || st.Collections[0].Files != 28 {
		t.Fatalf("unexpected first collection: %+v", st.Collections[0])
	}
	if st.Collections[1].Name != "beta" || st.Collections[1].Files != 67 {
		t.Fatalf("unexpected second collection: %+v", st.Collections[1])
	}
}

func TestParseSearchOutput_FilesCSV(t *testing.T) {
	out := "#a86f40,0.81,qmd://claw-memory/daily/2026-02-11.md,\"OpenClaw context\""
	results, err := parseSearchOutput(out)
	if err != nil {
		t.Fatalf("parseSearchOutput failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := results[0]
	if got.DocID != "#a86f40" || got.Score != 0.81 || got.Collection != "claw-memory" {
		t.Fatalf("unexpected files result: %+v", got)
	}
	if got.Title != "2026-02-11.md" {
		t.Fatalf("unexpected title: %q", got.Title)
	}
}

func TestParseSearchOutput_FilesCSVWithWarningLines(t *testing.T) {
	out := strings.Join([]string{
		"Warning: 10 docs need embeddings.",
		"#a1,0.50,qmd://alpha/a.md,\"ctx\"",
		"#b2,0.40,qmd://beta/b.md,\"ctx\"",
	}, "\n")
	results, err := parseSearchOutput(out)
	if err != nil {
		t.Fatalf("parseSearchOutput failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(results))
	}
	if results[0].Collection != "alpha" || results[1].Collection != "beta" {
		t.Fatalf("unexpected collection parse: %+v", results)
	}
}

func TestAppendSearchArgs_FilesOnly(t *testing.T) {
	args := appendSearchArgs([]string{"search", "q", "--json"}, SearchOpts{
		Collection: "alpha",
		N:          3,
		MinScore:   0.4,
		FilesOnly:  true,
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--files") {
		t.Fatalf("expected --files in args: %v", args)
	}
}

func TestAppendSearchArgs_FilesOnlyAllOmitsN(t *testing.T) {
	args := appendSearchArgs([]string{"search", "q", "--json"}, SearchOpts{
		Collection: "alpha",
		N:          99,
		FilesOnly:  true,
		All:        true,
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--files") {
		t.Fatalf("expected --files in args: %v", args)
	}
	if !strings.Contains(joined, "--all") {
		t.Fatalf("expected --all in args: %v", args)
	}
	if strings.Contains(joined, " -n ") {
		t.Fatalf("did not expect -n when --all is enabled: %v", args)
	}
}
