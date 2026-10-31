package model

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

func coverTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	t.Setenv("TERM_PROGRAM", "")
	engine := &playbackFakeEngine{playing: true}
	m := New(engine, playlist.New(), nil, "", nil, nil, nil, nil, nil, nil)
	m.width, m.height = width, height
	m.showMetadata = true
	// Most tests run in a plain test terminal; "on" keeps the block fallback
	// so they can check layout and rendering.
	m.SetCoverArtMode("on")
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
		{name: "auto without graphics", term: "xterm-256color", mode: "auto", w: 100, h: 24},
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

func writeCoverPNG(t *testing.T, name string, width, height int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatalf("encode cover: %v", err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func TestCoverColsKeepsAspect(t *testing.T) {
	tests := []struct {
		name                 string
		rows                 int
		aspect, cellW, cellH float64
		want                 int
	}{
		{name: "square in 1:2 cells", rows: 6, aspect: 1, cellW: 10, cellH: 20, want: 12},
		{name: "wide image", rows: 6, aspect: 2, cellW: 10, cellH: 20, want: 24},
		{name: "tall image", rows: 6, aspect: 0.5, cellW: 10, cellH: 20, want: 6},
		{name: "unknown cell size", rows: 5, aspect: 1, want: 10},
		{name: "never zero", rows: 1, aspect: 0.01, cellW: 10, cellH: 20, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coverCols(tt.rows, tt.aspect, tt.cellW, tt.cellH); got != tt.want {
				t.Fatalf("coverCols = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInfoCoverShowsHighlightedTrack(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 120, 30)
	playing := playlist.Track{Path: "playing.mp3", Title: "Playing", AlbumArtURL: writeCoverPNG(t, "playing.png", 4, 4)}
	selected := playlist.Track{
		Path: "/music/selected.mp3", Title: "Selected", Artist: "Artist", Album: "Album",
		Genre: "Genre", Year: 2026, TrackNumber: 3,
		AlbumArtURL: writeCoverPNG(t, "selected.png", 8, 4),
	}
	m.playlist.Replace([]playlist.Track{playing, selected})
	m.plCursor = 1
	m.info.visible = true

	load := m.ensureInfoCover()
	if load == nil || !m.infoCover.loading {
		t.Fatal("ensureInfoCover did not load the highlighted track's cover")
	}
	loaded, ok := load().(coverLoadedMsg)
	if !ok || loaded.slot != coverSlotInfo {
		t.Fatalf("load returned %#v, want an info-slot coverLoadedMsg", loaded)
	}
	m.handleCoverLoaded(loaded)
	if m.infoCover.aspect != 2 {
		t.Fatalf("info cover aspect = %v, want 2 for an 8x4 image", m.infoCover.aspect)
	}

	cols, rows, _, visible := m.infoCoverGeometry()
	if !visible {
		t.Fatal("info cover geometry is hidden in a wide terminal")
	}
	if fields := len(m.infoLines()); rows != min(fields, m.effectivePlaylistVisible()) {
		t.Fatalf("cover rows = %d, want the %d field lines", rows, fields)
	}
	cellW, cellH := m.cellSize()
	if want := coverCols(rows, 2, cellW, cellH); cols != want {
		t.Fatalf("cover cols = %d, want %d for a 2:1 image", cols, want)
	}

	render := m.ensureInfoCover()
	if render == nil || !m.infoCover.rendering {
		t.Fatal("ensureInfoCover did not render the loaded cover")
	}
	m.handleCoverRendered(render().(coverRenderedMsg))
	body := m.renderInfoBody()
	if !strings.Contains(body, "▀") || !strings.Contains(body, "Selected") {
		t.Fatalf("info body lacks the cover or the fields:\n%s", body)
	}
	if m.renderCover() != "" {
		t.Fatal("info cover leaked into the playing-track pane")
	}

	m.info.visible = false
	if cmd := m.ensureInfoCover(); cmd != nil || m.infoCover.key != "" || m.infoCover.render() != "" {
		t.Fatal("closing the info overlay kept its cover")
	}
}

func TestInfoCoverHiddenWhenTooNarrow(t *testing.T) {
	m := coverTestModel(t, 40, 30)
	m.playlist.Replace([]playlist.Track{{Path: "a.mp3", Title: "A", Artist: "B", Album: "C", Genre: "D", Year: 1, TrackNumber: 1}})
	m.info.visible = true
	m.infoCover = coverState{key: "k", local: "file:///cover.png", aspect: 1}
	if _, _, _, visible := m.infoCoverGeometry(); visible {
		t.Fatal("info cover shown in a body too narrow for the fields")
	}
	if got, want := m.infoTextWidth(), m.infoWidth(); got != want {
		t.Fatalf("text width = %d, want full body %d without a cover", got, want)
	}
}

// The full-screen visualizer shows no cover, even with Metadata on.
func TestFullVisShowsNoCover(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	m := coverTestModel(t, 120, 40)
	m.SetCoverArtMode("on")
	m.SetShowMetadata(true)
	m.cover.local = "file:///cover.png"
	m.recomputeLayout()
	if _, _, _, visible := m.coverGeometry(); !visible {
		t.Fatal("main view shows no cover")
	}
	m.fullVis = true
	m.recomputeLayout()
	if _, _, _, visible := m.coverGeometry(); visible {
		t.Fatal("full-screen visualizer shows a cover")
	}
}

func TestAutoCoverWithoutGraphicsKeepsLayout(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	m := coverTestModel(t, 120, 40)
	m.SetCoverArtMode("auto")
	m.cover.local = "file:///cover.png"
	m.recomputeLayout()
	if _, _, _, visible := m.coverGeometry(); visible {
		t.Fatal("auto drew a cover in a terminal without graphics")
	}
	if m.layout.coverRows != m.layout.baseVisualizerRows || m.layout.visualizerRows != m.layout.baseVisualizerRows {
		t.Fatalf("visualizer grew for a hidden cover: cover=%d vis=%d base=%d", m.layout.coverRows, m.layout.visualizerRows, m.layout.baseVisualizerRows)
	}

	m.playlist.Replace([]playlist.Track{{Path: "a.mp3", Title: "A", AlbumArtURL: "file:///cover.png"}})
	m.info.visible = true
	if cmd := m.ensureInfoCover(); cmd != nil || m.infoCover.key != "" {
		t.Fatal("auto loaded an info-overlay cover in a terminal without graphics")
	}
}

// listArtProvider serves covers for some browser entries.
type listArtProvider struct {
	commandsTestProvider
	art     map[string][]byte
	fetched []string
}

func (p *listArtProvider) CanFetchListArtwork(list playlist.PlaylistInfo) bool {
	return p.art[list.ID] != nil
}

func (p *listArtProvider) ListDetails(list playlist.PlaylistInfo) (provider.ListDescription, bool) {
	if list.ID == "c" {
		return provider.ListDescription{}, false
	}
	return provider.ListDescription{Title: "Title " + list.ID, Subtitle: "Artist", Lines: []string{"2013-06-28 · Label"}}, true
}

func (p *listArtProvider) FetchListArtwork(_ context.Context, list playlist.PlaylistInfo) ([]byte, error) {
	p.fetched = append(p.fetched, list.ID)
	return p.art[list.ID], nil
}

func coverPNGBytes(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode cover: %v", err)
	}
	return raw.Bytes()
}

func listCoverTestModel(t *testing.T) (Model, *listArtProvider) {
	t.Helper()
	// The cover cache lives in the data directory under HOME.
	t.Setenv("HOME", t.TempDir())
	m := coverTestModel(t, 120, 40)
	prov := &listArtProvider{
		commandsTestProvider: commandsTestProvider{name: "Plex"},
		art:                  map[string][]byte{"a": coverPNGBytes(t), "b": coverPNGBytes(t)},
	}
	m.provider = prov
	m.focus = focusProvider
	m.provPane.lists = []playlist.PlaylistInfo{{ID: "a", Name: "Album A"}, {ID: "b", Name: "Album B"}, {ID: "c", Name: "No Art"}}
	m.recomputeLayout()
	return m, prov
}

func TestListCoverLoadsAfterTheCursorRests(t *testing.T) {
	m, prov := listCoverTestModel(t)

	// Moving the cursor schedules a delayed load for each new entry.
	first := m.ensureListCover()
	firstDue, ok := first().(listCoverDueMsg)
	if !ok {
		t.Fatal("ensureListCover did not schedule the delayed load")
	}
	m.provPane.cursor = 1
	second := m.ensureListCover()
	secondDue := second().(listCoverDueMsg)

	// The first entry's timer fires after the cursor left it: no request.
	if cmd := m.handleListCoverDue(firstDue); cmd != nil || len(prov.fetched) != 0 {
		t.Fatalf("stale due message loaded a cover: fetched %v", prov.fetched)
	}
	load := m.handleListCoverDue(secondDue)
	if load == nil {
		t.Fatal("rested cursor did not load the cover")
	}
	loaded := load().(coverLoadedMsg)
	if loaded.slot != coverSlotList || loaded.err != nil {
		t.Fatalf("load = %+v", loaded)
	}
	m.handleCoverLoaded(loaded)
	if len(prov.fetched) != 1 || prov.fetched[0] != "b" {
		t.Fatalf("fetched = %v, want only b", prov.fetched)
	}

	render := m.ensureListCover()
	if render == nil {
		t.Fatal("loaded cover did not render")
	}
	m.handleCoverRendered(render().(coverRenderedMsg))

	cols, rows, _, ok := m.listCoverGeometry()
	if !ok || rows != min(m.layout.mainCoverRows, m.effectivePlaylistVisible()-listCoverTop) {
		t.Fatalf("geometry = %dx%d %v, main cover rows %d", cols, rows, ok, m.layout.mainCoverRows)
	}
	col, _ := m.listColumnGeometry()
	listWidth := col.listWidth
	lines := strings.Split(m.renderPlaylist(), "\n")
	for i := listCoverTop; i < listCoverTop+rows; i++ {
		column := ansi.Strip(ansi.Cut(lines[i], listWidth+columnGutterWidth, m.layout.panelWidth))
		if !strings.HasPrefix(column, "▀") || !strings.Contains(column, "▀") {
			t.Fatalf("line %d: cover does not start the column: %q", i, column)
		}
	}
	for i, line := range lines {
		if left := ansi.Truncate(line, listWidth, ""); strings.Contains(ansi.Strip(left), "▀") {
			t.Fatalf("line %d: cover reaches into the list: %q", i, ansi.Strip(line))
		}
	}
	column := ansi.Strip(strings.Join(lines[listCoverTop+rows:], "\n"))
	if !strings.Contains(column, "Title b") || !strings.Contains(column, "2013-06-28") {
		t.Fatalf("description missing under the cover:\n%s", column)
	}

	// An entry without artwork, or leaving the browser, drops the cover.
	m.provPane.cursor = 2
	if cmd := m.ensureListCover(); cmd != nil || m.listCover.key != "" {
		t.Fatal("entry without artwork kept a cover")
	}
	m.provPane.cursor = 0
	m.ensureListCover()
	m.focus = focusPlaylist
	if m.ensureListCover(); m.listCover.key != "" {
		t.Fatal("leaving the browser kept the cover")
	}
}

func TestListCoverMatchesMainViewSize(t *testing.T) {
	m, _ := listCoverTestModel(t)
	m.cover.local = "file:///playing.png"
	m.listCover.local = "file:///list.png"

	m.focus = focusPlaylist
	m.recomputeLayout()
	_, mainRows, _, mainOK := m.coverGeometry()

	m.focus = focusProvider
	m.recomputeLayout()
	_, rows, _, ok := m.listCoverGeometry()
	if !mainOK || !ok || rows != min(mainRows, m.effectivePlaylistVisible()-listCoverTop) {
		t.Fatalf("browser cover rows = %d (ok %v), main view rows = %d (ok %v)", rows, ok, mainRows, mainOK)
	}
}

// The browser cover keeps its size while no track cover is loaded, as when
// playback is stopped.
func TestListCoverSizeWithoutTrackCover(t *testing.T) {
	m, _ := listCoverTestModel(t)
	m.listCover.local = "file:///list.png"
	m.focus = focusProvider

	m.cover.local = "file:///playing.png"
	m.recomputeLayout()
	_, want, _, _ := m.listCoverGeometry()

	m.cover.local = ""
	m.recomputeLayout()
	if _, rows, _, ok := m.listCoverGeometry(); !ok || rows != want {
		t.Fatalf("browser cover rows without a track cover = %d (ok %v), want %d", rows, ok, want)
	}
}

func TestListCoverHiddenWithoutMetadataOrGraphics(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	for _, tt := range []struct {
		name string
		set  func(*Model)
	}{
		{name: "ctrl+i off", set: func(m *Model) { m.SetShowMetadata(false) }},
		{name: "auto without graphics", set: func(m *Model) { m.SetCoverArtMode("auto") }},
		{name: "cover_art off", set: func(m *Model) { m.SetCoverArtMode("off") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, prov := listCoverTestModel(t)
			tt.set(&m)
			if cmd := m.ensureListCover(); cmd != nil || len(prov.fetched) != 0 {
				t.Fatal("hidden browser cover scheduled a load")
			}
		})
	}
}

// Without terminal graphics the column keeps the description, starting under
// the one-line gap, and reserves no cover.
func TestListColumnWithoutGraphicsShowsDescription(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "")
	m, prov := listCoverTestModel(t)
	m.SetCoverArtMode("auto")
	if cmd := m.ensureListCover(); cmd != nil || len(prov.fetched) != 0 {
		t.Fatal("column without graphics loaded a cover")
	}
	col, ok := m.listColumnGeometry()
	if !ok {
		t.Fatal("column hidden for a describing provider")
	}
	lines := strings.Split(m.renderPlaylist(), "\n")
	if got := strings.TrimSpace(ansi.Strip(ansi.Cut(lines[listCoverTop], col.listWidth, m.layout.panelWidth))); got != "Title a" {
		t.Fatalf("first column line = %q, want the title (column %d wide)", got, col.width)
	}
}

// The browser column is as wide as the main view's settings pane, and the
// list keeps the main view's playlist width.
func TestListColumnMatchesSettingsPane(t *testing.T) {
	m, _ := listCoverTestModel(t)
	m.focus = focusPlaylist
	m.recomputeLayout()
	settings, playlistWidth := m.layout.settingsWidth, m.layout.playlistWidth

	m.focus = focusProvider
	m.recomputeLayout()
	col, ok := m.listColumnGeometry()
	if !ok || col.width != settings || col.listWidth != playlistWidth {
		t.Fatalf("column = %+v (ok %v), settings pane %d, playlist %d", col, ok, settings, playlistWidth)
	}
	if col.coverCols > col.width {
		t.Fatalf("cover %d cols overflows the %d-col column", col.coverCols, col.width)
	}
}

// The list header stops at the gutter and the column has its own header, as
// the settings pane does in the main view.
func TestListColumnHeader(t *testing.T) {
	m, _ := listCoverTestModel(t)
	col, _ := m.listColumnGeometry()
	header := m.renderPlaylistHeader()
	if got := lipgloss.Width(header); got != m.layout.panelWidth {
		t.Fatalf("header width = %d, want %d", got, m.layout.panelWidth)
	}
	list := ansi.Strip(ansi.Truncate(header, col.listWidth, ""))
	gutter := ansi.Strip(ansi.Cut(header, col.listWidth, col.listWidth+columnGutterWidth))
	column := ansi.Strip(ansi.Cut(header, col.listWidth+columnGutterWidth, m.layout.panelWidth))
	if !strings.Contains(list, "Plex / Playlists") || strings.TrimSpace(gutter) != "" || !strings.Contains(column, "Metadata") {
		t.Fatalf("header parts: list %q gutter %q column %q", list, gutter, column)
	}

	m.SetShowMetadata(false)
	if header := ansi.Strip(m.renderPlaylistHeader()); strings.Contains(header, "Metadata") {
		t.Fatalf("header without the column = %q", header)
	}
}

// Ctrl+I in the provider browser shows or hides the column in place and
// saves the preference, without opening the info overlay.
func TestListColumnCtrlIToggles(t *testing.T) {
	m, _ := listCoverTestModel(t)
	saver := &recordingSaver{}
	m.configSaver = saver
	for _, want := range []bool{false, true} {
		m.handleKey(tea.KeyPressMsg{Code: 'i', Mod: tea.ModCtrl})
		if m.showMetadata != want || m.info.visible || m.focus != focusProvider {
			t.Fatalf("Ctrl+I: metadata=%v info=%v focus=%v, want metadata %v", m.showMetadata, m.info.visible, m.focus, want)
		}
		if _, ok := m.listColumnGeometry(); ok != want {
			t.Fatalf("column shown = %v, want %v", ok, want)
		}
		if saver.saved["show_metadata"] != strconv.FormatBool(want) {
			t.Fatalf("saved = %v", saver.saved)
		}
	}
}
