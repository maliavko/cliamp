package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/cliamp/playlist"
)

func TestLatinKey(t *testing.T) {
	tests := []struct {
		name     string
		key      tea.KeyPressMsg
		nonLatin bool
		want     string
		ok       bool
	}{
		{name: "letter", key: tea.KeyPressMsg{Code: 'о', Text: "о"}, want: "j", ok: true},
		{name: "upper letter", key: tea.KeyPressMsg{Code: 'Щ', Text: "Щ"}, want: "O", ok: true},
		{name: "kitty shifted letter", key: tea.KeyPressMsg{Code: 'щ', ShiftedCode: 'Щ', Mod: tea.ModShift, Text: "Щ"}, want: "O", ok: true},
		{name: "punctuation", key: tea.KeyPressMsg{Code: 'ю', Text: "ю"}, want: ".", ok: true},
		{name: "upper punctuation", key: tea.KeyPressMsg{Code: 'Б', Text: "Б"}, want: "<", ok: true},
		{name: "ctrl", key: tea.KeyPressMsg{Code: 'щ', Mod: tea.ModCtrl}, want: "ctrl+o", ok: true},
		{name: "ukrainian", key: tea.KeyPressMsg{Code: 'і', Text: "і"}, want: "s", ok: true},
		{name: "base code", key: tea.KeyPressMsg{Code: 'ς', BaseCode: 'w', Text: "ς"}, want: "w", ok: true},
		{name: "latin", key: tea.KeyPressMsg{Code: 'j', Text: "j"}, want: "j"},
		{name: "unknown", key: tea.KeyPressMsg{Code: 'λ', Text: "λ"}, want: "λ"},
		{name: "russian dot on slash", key: tea.KeyPressMsg{Code: '.', BaseCode: '/', Text: "."}, nonLatin: true, want: "/", ok: true},
		{name: "russian comma on question", key: tea.KeyPressMsg{Code: '.', BaseCode: '/', ShiftedCode: ',', Mod: tea.ModShift, Text: ","}, nonLatin: true, want: "?", ok: true},
		{name: "russian shifted digit", key: tea.KeyPressMsg{Code: '4', ShiftedCode: ';', Mod: tea.ModShift, Text: ";"}, nonLatin: true, want: "$", ok: true},
		{name: "russian dot without protocol", key: tea.KeyPressMsg{Code: '.', Text: "."}, nonLatin: true, want: "."},
		{name: "latin layout punctuation", key: tea.KeyPressMsg{Code: '.', BaseCode: 'e', Text: "."}, want: "."},
		{name: "latin layout letter", key: tea.KeyPressMsg{Code: 'h', BaseCode: 'j', Text: "h"}, nonLatin: true, want: "h"},
		{name: "same key", key: tea.KeyPressMsg{Code: '9', ShiftedCode: '(', Mod: tea.ModShift, Text: "("}, nonLatin: true, want: "("},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := latinKey(tt.key, tt.nonLatin)
			if ok != tt.ok || got.String() != tt.want {
				t.Fatalf("latinKey = %q, %v; want %q, %v", got.String(), ok, tt.want, tt.ok)
			}
		})
	}
}

// A hotkey works in the Russian layout, and a text input still types Russian.
func TestKeysInRussianLayout(t *testing.T) {
	m := newColumnTestModel(80, 24)
	m.playlist.Replace([]playlist.Track{{Path: "a.mp3"}, {Path: "b.mp3"}, {Path: "c.mp3"}})
	m.focus, m.plCursor = focusPlaylist, 0

	m.handleKey(tea.KeyPressMsg{Code: 'о', Text: "о"})
	if m.plCursor != 1 {
		t.Fatalf("cursor = %d after о (j), want 1", m.plCursor)
	}

	m.handleKey(tea.KeyPressMsg{Code: '.', BaseCode: '/', Text: "."})
	if !m.search.active {
		t.Fatal("the / key, which types . in Russian, opened no search")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'п', Text: "п"})
	m.handleKey(tea.KeyPressMsg{Code: 'Ё', Text: "Ё"})
	m.handleKey(tea.KeyPressMsg{Code: '.', BaseCode: '/', Text: "."})
	if m.search.query != "пЁ." {
		t.Fatalf("search query = %q, want пЁ.", m.search.query)
	}
}
