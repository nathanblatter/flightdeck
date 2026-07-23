// Package metrics is a tiny, dependency-free Prometheus text-format exporter —
// just enough for RED (rate/errors/duration) on the HTTP and MCP surfaces so
// performance work can be measured instead of guessed. A global registry keeps
// the call sites trivial; a mutex is fine at flightdeck's request volume.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type hist struct {
	counts []uint64
	sum    float64
	n      uint64
}

func newHist() *hist { return &hist{counts: make([]uint64, len(buckets))} }

// observe increments only the first bucket the value fits in (counts are
// per-bucket, NOT cumulative — WritePrometheus accumulates at render time).
// Values beyond the last bucket land only in the implicit +Inf (h.n).
func (h *hist) observe(sec float64) {
	for i, b := range buckets {
		if sec <= b {
			h.counts[i]++
			break
		}
	}
	h.sum += sec
	h.n++
}

type metric struct {
	name, help string
	mu         sync.Mutex
	counters   map[string]uint64 // labelset -> count
	hists      map[string]*hist  // labelset -> histogram
}

var (
	httpReq = &metric{name: "flightdeck_http_requests_total", help: "HTTP requests by route and status.", counters: map[string]uint64{}}
	httpDur = &metric{name: "flightdeck_http_request_duration_seconds", help: "HTTP request latency by route.", hists: map[string]*hist{}}
	mcpReq  = &metric{name: "flightdeck_mcp_tool_calls_total", help: "MCP tool calls by tool and result.", counters: map[string]uint64{}}
	mcpDur  = &metric{name: "flightdeck_mcp_tool_duration_seconds", help: "MCP tool latency by tool.", hists: map[string]*hist{}}
	srchDur = &metric{name: "flightdeck_search_duration_seconds", help: "Search latency by recall path (lexical|cache|embed).", hists: map[string]*hist{}}
	all     = []*metric{httpReq, httpDur, mcpReq, mcpDur, srchDur}
)

// HTTP records one served request.
func HTTP(method, route string, status int, d time.Duration) {
	httpReq.incr(fmt.Sprintf(`method=%q,route=%q,status="%d"`, method, route, status))
	httpDur.observe(fmt.Sprintf(`method=%q,route=%q`, method, route), d)
}

// MCP records one tool invocation.
func MCP(tool string, ok bool, d time.Duration) {
	result := "ok"
	if !ok {
		result = "error"
	}
	mcpReq.incr(fmt.Sprintf(`tool=%q,result=%q`, tool, result))
	mcpDur.observe(fmt.Sprintf(`tool=%q`, tool), d)
}

// Search records one search, labeled by the recall path that determined its
// latency: "lexical" (no embed — fast path), "cache" (query vector served from
// Redis, no OpenAI call), or "embed" (paid the live OpenAI embedding round-trip).
// This is what tells a lazy-skip from a cache hit from a genuine embed.
func Search(path string, d time.Duration) {
	srchDur.observe(fmt.Sprintf(`path=%q`, path), d)
}

func (m *metric) incr(labels string) {
	m.mu.Lock()
	m.counters[labels]++
	m.mu.Unlock()
}

func (m *metric) observe(labels string, d time.Duration) {
	m.mu.Lock()
	h := m.hists[labels]
	if h == nil {
		h = newHist()
		m.hists[labels] = h
	}
	h.observe(d.Seconds())
	m.mu.Unlock()
}

// WritePrometheus renders all metrics in the Prometheus text exposition format.
func WritePrometheus(w io.Writer) {
	for _, m := range all {
		m.mu.Lock()
		if len(m.counters) == 0 && len(m.hists) == 0 {
			m.mu.Unlock()
			continue
		}
		typ := "counter"
		if len(m.hists) > 0 {
			typ = "histogram"
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, typ)
		for _, ls := range sortedKeys(m.counters) {
			fmt.Fprintf(w, "%s{%s} %d\n", m.name, ls, m.counters[ls])
		}
		for _, ls := range sortedKeysH(m.hists) {
			h := m.hists[ls]
			cum := uint64(0)
			for i, b := range buckets {
				cum += h.counts[i]
				fmt.Fprintf(w, "%s_bucket{%s,le=\"%s\"} %d\n", m.name, ls, strconv.FormatFloat(b, 'g', -1, 64), cum)
			}
			fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n", m.name, ls, h.n)
			fmt.Fprintf(w, "%s_sum{%s} %s\n", m.name, ls, strconv.FormatFloat(h.sum, 'g', -1, 64))
			fmt.Fprintf(w, "%s_count{%s} %d\n", m.name, ls, h.n)
		}
		m.mu.Unlock()
	}
}

func sortedKeys(m map[string]uint64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedKeysH(m map[string]*hist) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
