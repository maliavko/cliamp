package model

import (
	"slices"
	"testing"

	"github.com/bjarneo/cliamp/playlist"
)

func TestUpdateProvSearchFuzzy(t *testing.T) {
	lists := []playlist.PlaylistInfo{
		{Name: "Megadeth — Rust in Peace (1990)"},
		{Name: "Metallica — Reload (1997)"},
		{Name: "Metallica — Load (1996)"},
		{Name: "Mr. Big — Bump Ahead (1993)"},
	}
	tests := []struct {
		query string
		want  []string
	}{
		{query: "metallica reload", want: []string{"Metallica — Reload (1997)"}},
		{query: "mtl", want: []string{"Metallica — Reload (1997)", "Metallica — Load (1996)"}},
		{query: "RELOAD", want: []string{"Metallica — Reload (1997)"}},
		{query: "zzz"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			m := Model{}
			m.provPane.lists = lists
			m.provSearch.query = tt.query
			m.updateProvSearch()
			var got []string
			for _, i := range m.provSearch.results {
				got = append(got, lists[i].Name)
			}
			// internal/fuzzy tests the ranking; this checks which entries match.
			slices.Sort(got)
			slices.Sort(tt.want)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("results = %q, want %q", got, tt.want)
			}
		})
	}
}
