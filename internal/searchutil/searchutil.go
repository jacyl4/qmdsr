package searchutil

import (
	"fmt"
	"sort"
	"strings"

	"qmdsr/model"
)

// DedupSortLimit removes duplicate hits, sorts by score desc, and applies topK.
func DedupSortLimit(results []model.SearchResult, topK int) []model.SearchResult {
	seen := make(map[string]struct{}, len(results))
	deduped := make([]model.SearchResult, 0, len(results))
	for _, r := range results {
		key := strings.TrimSpace(r.DocID)
		if key == "" {
			key = strings.TrimSpace(r.File)
		}
		if key == "" {
			key = fmt.Sprintf("%s|%s|%0.4f", r.Title, r.Snippet, r.Score)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, r)
	}

	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].Score > deduped[j].Score
	})

	if topK > 0 {
		const maxPerFile = 2
		// This limit mainly improves snippet diversity. In files_only mode qmd already
		// returns file-level rows, so duplicates are typically absent before this stage.
		fileCounts := make(map[string]int, len(deduped))
		diverse := make([]model.SearchResult, 0, topK)
		for _, r := range deduped {
			file := strings.TrimSpace(r.File)
			if file != "" && fileCounts[file] >= maxPerFile {
				continue
			}
			if file != "" {
				fileCounts[file]++
			}
			diverse = append(diverse, r)
			if len(diverse) >= topK {
				break
			}
		}
		return diverse
	}
	return deduped
}
