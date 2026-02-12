package model

import "time"

type SearchResult struct {
	Title      string  `json:"title"`
	File       string  `json:"file"`
	Collection string  `json:"collection"`
	Score      float64 `json:"score"`
	Snippet    string  `json:"snippet"`
	DocID      string  `json:"docid"`
}

type SearchMeta struct {
	ModeUsed            string   `json:"mode_used"`
	ServedMode          string   `json:"served_mode,omitempty"`
	CollectionsSearched []string `json:"collections_searched"`
	FallbackTriggered   bool     `json:"fallback_triggered"`
	CacheHit            bool     `json:"cache_hit"`
	Degraded            bool     `json:"degraded"`
	DegradeReason       string   `json:"degrade_reason,omitempty"`
	TraceID             string   `json:"trace_id,omitempty"`
	LatencyMs           int64    `json:"latency_ms"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Meta    SearchMeta     `json:"meta"`
}

type Document struct {
	File    string `json:"file"`
	Content string `json:"content"`
}

type CollectionInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Mask  string `json:"mask"`
	Files int    `json:"files"`
}

type PathContext struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

type IndexStatus struct {
	Collections []CollectionInfo `json:"collections"`
	Vectors     int              `json:"vectors"`
	LastUpdate  time.Time        `json:"last_update"`
	Raw         string           `json:"raw"`
}

type HealthLevel int

const (
	Healthy   HealthLevel = 0
	Degraded  HealthLevel = 1
	Unhealthy HealthLevel = 2
	Critical  HealthLevel = 3
)

func (h HealthLevel) String() string {
	switch h {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	case Unhealthy:
		return "unhealthy"
	case Critical:
		return "critical"
	default:
		return "unknown"
	}
}

type ComponentHealth struct {
	Name        string      `json:"name"`
	Level       HealthLevel `json:"level"`
	LevelStr    string      `json:"level_str"`
	LastCheck   time.Time   `json:"last_check"`
	LastHealthy time.Time   `json:"last_healthy"`
	Message     string      `json:"message"`
	FailCount   int         `json:"fail_count"`
}

type SystemHealth struct {
	Overall    HealthLevel                 `json:"overall"`
	OverallStr string                      `json:"overall_str"`
	Components map[string]*ComponentHealth `json:"components"`
	StartedAt  time.Time                   `json:"started_at"`
	UptimeSec  int64                       `json:"uptime_sec"`
	Mode       string                      `json:"mode"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Details   map[string]string `json:"details,omitempty"`
}

type MemoryWriteRequest struct {
	Topic      string `json:"topic"`
	Summary    string `json:"summary"`
	Source     string `json:"source"`
	Importance string `json:"importance"`
	LongTerm   bool   `json:"long_term"`
}

type StateUpdateRequest struct {
	Goal       string `json:"goal"`
	Progress   string `json:"progress"`
	Facts      string `json:"facts"`
	OpenIssues string `json:"open_issues"`
	Next       string `json:"next"`
}

type SearchRequest struct {
	Query      string  `json:"query"`
	Mode       string  `json:"mode"`
	Collection string  `json:"collection"`
	N          int     `json:"n"`
	MinScore   float64 `json:"min_score"`
	Fallback   *bool   `json:"fallback"`
	Format     string  `json:"format"`
	Confirm    bool    `json:"confirm"`
}

type GetRequest struct {
	Ref         string `json:"ref"`
	Full        bool   `json:"full"`
	LineNumbers bool   `json:"line_numbers"`
}

type MultiGetRequest struct {
	Pattern  string `json:"pattern"`
	MaxBytes int    `json:"max_bytes"`
}
