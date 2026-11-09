package service

import (
	"testing"
	"time"
)

func TestParseRecur(t *testing.T) {
	cases := []struct {
		in       string
		interval bool
		n        int
		unit     byte
		rule     string
		wantErr  bool
	}{
		{"P1Y", true, 1, 'Y', "P1Y", false},
		{" p6m ", true, 6, 'M', "P6M", false},
		{"P10D", true, 10, 'D', "P10D", false},
		{"P2W", true, 2, 'W', "P2W", false},
		{"P0D", false, 0, 0, "", true},
		{"", false, 0, 0, "", true},
		{"   ", false, 0, 0, "", true},
		{"at 72,900 mi", false, 0, 0, "at 72,900 mi", false},
		{"every even year in October", false, 0, 0, "every even year in October", false},
		{"Periodically", false, 0, 0, "Periodically", false},
		{"PT1H", false, 0, 0, "PT1H", false},
	}
	for _, c := range cases {
		r, err := ParseRecur(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRecur(%q): want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRecur(%q): %v", c.in, err)
			continue
		}
		if r.Interval != c.interval || r.N != c.n || r.Unit != c.unit || r.Rule != c.rule {
			t.Errorf("ParseRecur(%q) = %+v, want interval=%v n=%d unit=%c rule=%q", c.in, r, c.interval, c.n, c.unit, c.rule)
		}
	}
	long := make([]byte, recurMaxChars+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := ParseRecur(string(long)); err == nil {
		t.Errorf("ParseRecur(201 chars): want error")
	}
}

func TestRecurNext(t *testing.T) {
	base := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"P1Y":  time.Date(2027, 10, 31, 0, 0, 0, 0, time.UTC),
		"P6M":  time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC), // Apr 31 normalises
		"P10D": time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
		"P2W":  time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
	}
	for rule, want := range cases {
		r, err := ParseRecur(rule)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Next(base); !got.Equal(want) {
			t.Errorf("%s.Next(%s) = %s, want %s", rule, base, got, want)
		}
	}
	// A base in a DST-observing zone must not drift the instant across a
	// DST boundary (pgx hands timestamptz back in the server's local zone).
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip("no tzdata")
	}
	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	r, _ := ParseRecur("P6M")
	if got, want := r.Next(jan.In(denver)), time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("P6M across DST = %s, want %s", got, want)
	}
}

func TestRecurParam(t *testing.T) {
	if v, clear, err := RecurParam(nil); v != nil || clear || err != nil {
		t.Errorf("nil: got %v %v %v", v, clear, err)
	}
	empty := ""
	if v, clear, err := RecurParam(&empty); v != nil || !clear || err != nil {
		t.Errorf("empty: got %v %v %v", v, clear, err)
	}
	lower := "p1y"
	if v, clear, err := RecurParam(&lower); v == nil || *v != "P1Y" || clear || err != nil {
		t.Errorf("p1y: got %v %v %v", v, clear, err)
	}
	bad := "P0Y"
	if _, _, err := RecurParam(&bad); err == nil {
		t.Errorf("P0Y: want error")
	}
}
