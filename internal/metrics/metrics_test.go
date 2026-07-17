package metrics

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// Histogram buckets must be cumulative, non-decreasing, and end exactly at the
// +Inf/_count value — the invariant Prometheus' histogram_quantile depends on.
// Regression test for the double-cumulation bug where a single observation
// inflated every later bucket past +Inf.
func TestHistogramBucketsAreValid(t *testing.T) {
	MCP("histcheck", true, 3*time.Millisecond)   // → le=0.005
	MCP("histcheck", true, 700*time.Millisecond) // → le=1
	MCP("histcheck", true, 42*time.Second)       // beyond last bucket → only +Inf

	var buf bytes.Buffer
	WritePrometheus(&buf)

	re := regexp.MustCompile(`flightdeck_mcp_tool_duration_seconds_bucket\{tool="histcheck",le="([^"]+)"\} (\d+)`)
	matches := re.FindAllStringSubmatch(buf.String(), -1)
	if len(matches) != len(buckets)+1 {
		t.Fatalf("got %d bucket lines, want %d", len(matches), len(buckets)+1)
	}
	prev := uint64(0)
	for _, m := range matches {
		n, _ := strconv.ParseUint(m[2], 10, 64)
		if n < prev {
			t.Fatalf("bucket le=%s decreased: %d after %d", m[1], n, prev)
		}
		prev = n
	}
	last := matches[len(matches)-1]
	if last[1] != "+Inf" || last[2] != "3" {
		t.Fatalf("+Inf bucket = %s (le=%s), want 3 (every observation)", last[2], last[1])
	}
	// Spot-check exact cumulative values: 1 obs ≤ 0.005, 2 obs ≤ 1.
	for _, want := range []struct{ le, n string }{{"0.005", "1"}, {"1", "2"}, {"10", "2"}} {
		line := fmt.Sprintf(`le="%s"} %s`, want.le, want.n)
		if !bytes.Contains(buf.Bytes(), []byte(fmt.Sprintf(`flightdeck_mcp_tool_duration_seconds_bucket{tool="histcheck",%s`, line))) {
			t.Fatalf("missing expected bucket line %s in:\n%s", line, buf.String())
		}
	}
}
