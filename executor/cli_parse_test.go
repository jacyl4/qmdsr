package executor

import "testing"

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
