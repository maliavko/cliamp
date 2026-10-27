package model

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bjarneo/cliamp/playlist"
)

func coverTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	t.Setenv("TERM_PROGRAM", "")
	engine := &playbackFakeEngine{playing: true}
	m := New(engine, playlist.New(), nil, "", nil, nil, nil, nil, nil, nil)
	m.width, m.height = width, height
	m.showMetadata = true
	m.recomputeLayout()
	return m
}

func TestCoverGeometryModes(t *testing.T) {
	tests := []struct {
		name  string
		term  string
		mode  string
		w, h  int
		want  bool
		proto string
	}{
		{name: "auto with kitty", term: "xterm-kitty", mode: "auto", w: 100, h: 24, want: true, proto: "kitty"},
		{name: "auto block fallback", term: "xterm-256color", mode: "auto", w: 100, h: 24, want: true, proto: "blocks"},
		{name: "forced block fallback", term: "xterm-256color", mode: "on", w: 100, h: 24, want: true, proto: "blocks"},
		{name: "disabled", term: "xterm-kitty", mode: "off", w: 100, h: 24},
		{name: "compact layout", term: "xterm-kitty", mode: "on", w: 70, h: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TERM", tt.term)
			m := coverTestModel(t, tt.w, tt.h)
			m.SetCoverArtMode(tt.mode)
			cols, rows, protocol, visible := m.coverGeometry()
			if visible != tt.want || protocol != tt.proto {
				t.Fatalf("coverGeometry = (%d,%d,%q,%v), want protocol %q visible %v", cols, rows, protocol, visible, tt.proto, tt.want)
			}
			if visible && (cols != rows*2 || rows != m.layout.visualizerRows) {
				t.Fatalf("cover dimensions = %dx%d, visualizer rows = %d", cols, rows, m.layout.visualizerRows)
			}
		})
	}
}

func TestSetCoverArtModeNormalizesInvalidValue(t *testing.T) {
	m := Model{}
	m.SetCoverArtMode(" ON ")
	if got := m.coverArtMode(); got != "on" {
		t.Fatalf("mode = %q, want on", got)
	}
	m.SetCoverArtMode("unexpected")
	if got := m.coverArtMode(); got != "auto" {
		t.Fatalf("mode = %q, want auto", got)
	}
}

func TestCoverFollowsMetadataVisibility(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 120, 40)
	m.SetCoverArtMode("auto")
	m.cover.local = "file:///cover.png"
	m.recomputeLayout()

	cols, rows, protocol, visible := m.coverGeometry()
	if !visible || protocol != "blocks" {
		t.Fatalf("coverGeometry = (%d,%d,%q,%v)", cols, rows, protocol, visible)
	}
	if rows != 2*m.layout.baseVisualizerRows || rows != m.layout.visualizerRows || cols != 2*rows {
		t.Fatalf("cover = %dx%d, base visualizer rows = %d, visualizer rows = %d", cols, rows, m.layout.baseVisualizerRows, m.layout.visualizerRows)
	}

	m.SetShowMetadata(false)
	if cols, rows, protocol, visible := m.coverGeometry(); visible || cols != 0 || rows != 0 || protocol != "" {
		t.Fatalf("hidden metadata coverGeometry = (%d,%d,%q,%v)", cols, rows, protocol, visible)
	}

	m.SetShowMetadata(true)
	m.SetHideSettingsPane(true)
	if _, _, _, visible := m.coverGeometry(); visible {
		t.Fatal("cover is visible while the metadata sidebar is hidden")
	}
}

func TestLargeCoverKeepsMetadataVisible(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 80, 24)
	m.cover.local = "file:///cover.png"
	m.recomputeLayout()

	if _, _, _, visible := m.coverGeometry(); !visible {
		t.Fatal("cover is hidden in the minimum full layout")
	}
	if rows := m.metadataPaneRows(m.effectivePlaylistVisible()); rows < metadataPaneMinRows {
		t.Fatalf("metadata rows = %d, want at least %d", rows, metadataPaneMinRows)
	}
}

func TestSquareCoverColsUsesTerminalCellAspect(t *testing.T) {
	tests := []struct {
		name              string
		rows              int
		cellWidth, height float64
		want              int
	}{
		{name: "unknown geometry", rows: 14, want: 28},
		{name: "one to two cells", rows: 14, cellWidth: 10, height: 20, want: 28},
		{name: "taller cells", rows: 14, cellWidth: 14, height: 30, want: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := squareCoverCols(tt.rows, tt.cellWidth, tt.height); got != tt.want {
				t.Fatalf("squareCoverCols() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStaleCoverLoadIsIgnored(t *testing.T) {
	m := Model{cover: coverState{key: "new", loadGen: 2, loading: true}}
	m.handleCoverLoaded(coverLoadedMsg{key: "old", gen: 1, local: "file:///old.png"})
	if m.cover.local != "" || !m.cover.loading {
		t.Fatalf("stale load changed state: %+v", m.cover)
	}
	m.handleCoverLoaded(coverLoadedMsg{key: "new", gen: 2, local: "file:///new.png"})
	if m.cover.local != "file:///new.png" || m.cover.loading {
		t.Fatalf("current load was not applied: %+v", m.cover)
	}
}

func TestCoverRenderReadiness(t *testing.T) {
	tests := []struct {
		name     string
		state    coverState
		protocol string
		want     bool
	}{
		{name: "kitty placement", state: coverState{cols: 28, rows: 14, protocol: "kitty", imageID: 1001}, protocol: "kitty", want: true},
		{name: "kitty missing image", state: coverState{cols: 28, rows: 14, protocol: "kitty"}, protocol: "kitty"},
		{name: "block image", state: coverState{cols: 28, rows: 14, protocol: "blocks", block: "▀"}, protocol: "blocks", want: true},
		{name: "wrong dimensions", state: coverState{cols: 14, rows: 7, protocol: "kitty", imageID: 1001}, protocol: "kitty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.renderedAt(28, 14, tt.protocol); got != tt.want {
				t.Fatalf("renderedAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCoverLoadsAndRendersWithoutBlockingUpdate(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 100, 24)
	m.SetCoverArtMode("on")

	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatalf("encode cover: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cover art.png")
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	m.playlist.Replace([]playlist.Track{{Path: "song.mp3", AlbumArtURL: source}})

	load := m.ensureCover()
	if load == nil || !m.cover.loading {
		t.Fatal("ensureCover did not schedule a load")
	}
	loadMsg := load()
	loaded, ok := loadMsg.(coverLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T", loadMsg)
	}
	m.handleCoverLoaded(loaded)
	m.recomputeLayout()
	if got := m.artworkURL(playlist.Track{Path: "song.mp3", AlbumArtURL: source}); got != source {
		t.Fatalf("artworkURL = %q, want %q", got, source)
	}

	render := m.ensureCover()
	if render == nil || !m.cover.rendering {
		t.Fatal("ensureCover did not schedule a render")
	}
	renderMsg := render()
	rendered, ok := renderMsg.(coverRenderedMsg)
	if !ok {
		t.Fatalf("render command returned %T", renderMsg)
	}
	m.handleCoverRendered(rendered)
	if m.cover.protocol != "blocks" || !strings.Contains(m.renderCover(), "▀") {
		t.Fatalf("rendered cover protocol=%q block=%q", m.cover.protocol, m.renderCover())
	}
}

func TestArtworkLoadsWhileCoverPaneIsHidden(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 100, 24)
	m.SetShowMetadata(false)
	source := (&url.URL{Scheme: "file", Path: "/tmp/notification-cover.png"}).String()
	track := playlist.Track{Path: "song.mp3", AlbumArtURL: source}
	m.playlist.Replace([]playlist.Track{track})

	load := m.ensureCover()
	if load == nil || !m.cover.loading {
		t.Fatal("hidden cover pane did not schedule artwork loading")
	}
	loaded, ok := load().(coverLoadedMsg)
	if !ok {
		t.Fatal("artwork load returned the wrong message type")
	}
	m.handleCoverLoaded(loaded)
	if got := m.artworkURL(track); got != source {
		t.Fatalf("artworkURL = %q, want %q", got, source)
	}
	if cmd := m.ensureCover(); cmd != nil || m.renderCover() != "" {
		t.Fatal("hidden cover pane rendered artwork")
	}
}

func TestArtworkURLHidesRemoteSourceUntilCached(t *testing.T) {
	m := coverTestModel(t, 100, 24)
	track := playlist.Track{Path: "song.mp3", AlbumArtURL: "https://example.com/cover.jpg?token=secret"}
	if got := m.artworkURL(track); got != "" {
		t.Fatalf("uncached remote artwork URL leaked as %q", got)
	}
	m.cover.key = track.Path + "\x00" + track.AlbumArtURL
	m.cover.local = "file:///tmp/cached-cover.jpg"
	if got := m.artworkURL(track); got != m.cover.local {
		t.Fatalf("cached artworkURL = %q, want %q", got, m.cover.local)
	}
}
