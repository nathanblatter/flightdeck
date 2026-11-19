package main

import (
	"slices"
	"testing"
)

func TestSplitProjectsFlag(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		pos      []string
		projects []string
		found    bool
		wantErr  bool
	}{
		{"legacy positional unchanged", []string{"agent", "read,write", "30"}, []string{"agent", "read,write", "30"}, nil, false, false},
		{"flag at end", []string{"evals", "ingest", "--projects", "ai-therapist,other"}, []string{"evals", "ingest"}, []string{"ai-therapist", "other"}, true, false},
		{"flag between positionals", []string{"evals", "--projects=ai-therapist", "ingest", "90"}, []string{"evals", "ingest", "90"}, []string{"ai-therapist"}, true, false},
		{"missing value", []string{"evals", "ingest", "--projects"}, nil, nil, false, true},
		{"empty value", []string{"evals", "ingest", "--projects="}, nil, nil, false, true},
		{"repeated", []string{"e", "ingest", "--projects", "a", "--projects", "b"}, nil, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pos, projects, found, err := splitProjectsFlag(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !slices.Equal(pos, tc.pos) || !slices.Equal(projects, tc.projects) || found != tc.found {
				t.Errorf("got pos=%v projects=%v found=%v", pos, projects, found)
			}
		})
	}
}
