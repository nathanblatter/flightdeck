package api

import "testing"

func TestSeverityToPriority(t *testing.T) {
	cases := map[string]string{
		"low":      "low",
		"LOW":      "low",
		"  high ":  "high",
		"urgent":   "urgent",
		"critical": "urgent",
		"":         "med",
		"weird":    "med",
	}
	for in, want := range cases {
		if got := severityToPriority(in); got != want {
			t.Errorf("severityToPriority(%q) = %q, want %q", in, got, want)
		}
	}
}
