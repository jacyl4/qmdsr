package cache

import "testing"

func TestMakeCacheKey_DiffersByFilesAll(t *testing.T) {
	a := MakeCacheKey("q", "search", "alpha", 0.3, 8, true, true, false)
	b := MakeCacheKey("q", "search", "alpha", 0.3, 8, true, true, true)
	if a == b {
		t.Fatalf("expected distinct cache key when files_all differs")
	}
}
