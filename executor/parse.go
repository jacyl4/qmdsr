package executor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"qmdsr/model"
)

func parseCollectionListJSON(out string) ([]model.CollectionInfo, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return []model.CollectionInfo{}, nil
	}

	var cols []model.CollectionInfo
	if err := json.Unmarshal([]byte(trimmed), &cols); err == nil {
		return cols, nil
	}

	var wrapped struct {
		Collections []model.CollectionInfo `json:"collections"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && wrapped.Collections != nil {
		return wrapped.Collections, nil
	}

	return nil, fmt.Errorf("invalid json output")
}

func parseCollectionListText(out string) ([]model.CollectionInfo, error) {
	lines := strings.Split(out, "\n")
	cols := make([]model.CollectionInfo, 0, 8)
	indexByName := make(map[string]int)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "Collections") {
			continue
		}

		switch {
		case strings.Contains(line, " (qmd://") && strings.HasSuffix(line, ")"):
			name := strings.TrimSpace(strings.SplitN(line, " (qmd://", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(cols)
				cols = append(cols, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "qmd://") && strings.Contains(line, "/"):
			rest := strings.TrimPrefix(line, "qmd://")
			name := strings.TrimSpace(strings.SplitN(rest, "/", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(cols)
				cols = append(cols, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "Pattern:"):
			if len(cols) == 0 {
				continue
			}
			cols[len(cols)-1].Mask = strings.TrimSpace(strings.TrimPrefix(line, "Pattern:"))
		case strings.HasPrefix(line, "Files:"):
			if len(cols) == 0 {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "Files:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			cols[len(cols)-1].Files = n
		}
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("no collections parsed")
	}
	return cols, nil
}

func parseStatusJSON(out string) (*model.IndexStatus, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, fmt.Errorf("empty status output")
	}

	var status model.IndexStatus
	if err := json.Unmarshal([]byte(trimmed), &status); err == nil {
		return &status, nil
	}

	var wrapped struct {
		Status *model.IndexStatus `json:"status"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && wrapped.Status != nil {
		return wrapped.Status, nil
	}

	var generic map[string]any
	if err := json.Unmarshal([]byte(trimmed), &generic); err != nil {
		return nil, fmt.Errorf("invalid json output")
	}

	st := &model.IndexStatus{}
	if v, ok := intFromAny(generic["vectors"]); ok {
		st.Vectors = v
		return st, nil
	}

	if docs, ok := generic["documents"].(map[string]any); ok {
		if v, ok := intFromAny(docs["vectors"]); ok {
			st.Vectors = v
			return st, nil
		}
	}

	return nil, fmt.Errorf("vectors field not found")
}

func parseStatusText(out string) (*model.IndexStatus, error) {
	lines := strings.Split(out, "\n")
	st := &model.IndexStatus{
		Collections: make([]model.CollectionInfo, 0, 8),
	}
	indexByName := make(map[string]int)
	parsedVectors := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "Vectors:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "Vectors:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			v, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			st.Vectors = v
			parsedVectors = true
		case strings.Contains(line, " (qmd://") && strings.HasSuffix(line, ")"):
			name := strings.TrimSpace(strings.SplitN(line, " (qmd://", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(st.Collections)
				st.Collections = append(st.Collections, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "Files:"):
			if len(st.Collections) == 0 {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "Files:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			st.Collections[len(st.Collections)-1].Files = n
		}
	}

	if !parsedVectors {
		return nil, fmt.Errorf("vectors line not found")
	}
	return st, nil
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
