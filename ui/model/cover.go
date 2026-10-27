package model

import (
	"context"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/cliamp/applog"
	"github.com/bjarneo/cliamp/internal/artcache"
	"github.com/bjarneo/cliamp/internal/kittyimg"
	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

const (
	coverLoadTimeout = 30 * time.Second
	coverMinRows     = 4
)

type coverState struct {
	mode string

	key       string
	local     string
	failed    bool
	loading   bool
	loadGen   uint64
	rendering bool
	renderGen uint64

	cols     int
	rows     int
	protocol string
	block    string
	imageID  int

	termCols   int
	termRows   int
	cellWidth  float64
	cellHeight float64
}

type coverLoadedMsg struct {
	key   string
	local string
	gen   uint64
	err   error
}

type coverRenderedMsg struct {
	key      string
	gen      uint64
	cols     int
	rows     int
	protocol string
	block    string
	imageID  int
	err      error
}

// SetCoverArtMode sets native cover rendering to auto, on, or off. Auto picks
// Kitty graphics when advertised and ANSI half blocks everywhere else.
func (m *Model) SetCoverArtMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "on", "off":
	default:
		mode = "auto"
	}
	m.cover.mode = mode
}

func (m Model) coverArtMode() string {
	if m.cover.mode == "" {
		return "auto"
	}
	return m.cover.mode
}

// ensureCover schedules at most one artwork load or render. Artwork is loaded
// even while the TUI pane is hidden so media controls and desktop notifications
// can reuse the same local cache. Update calls it after applying layout and
// playback changes, so commands never block the UI.
func (m *Model) ensureCover() tea.Cmd {
	cols, rows, protocol, visible := m.coverGeometry()
	if !visible {
		m.clearCoverRender()
	}
	track, _ := m.currentPlaybackTrack()
	if m.player == nil || (!m.player.IsPlaying() && !m.buffering) || track.Path == "" {
		m.resetCover()
		return nil
	}

	source := m.baseArtworkURL(track)
	var fetcher provider.ArtworkFetcher
	if source == "" {
		fetcher, _ = findCapable(m, func(candidate provider.ArtworkFetcher) bool {
			return candidate.CanFetchArtwork(track)
		})
	}
	key := track.Path + "\x00" + source
	if fetcher != nil {
		key = track.Path + "\x00provider"
	}
	if source == "" && fetcher == nil {
		m.resetCover()
		return nil
	}
	if key != m.cover.key {
		m.clearCoverRender()
		m.cover.key = key
		m.cover.local = ""
		m.cover.failed = false
		m.cover.loading = true
		m.cover.loadGen++
		return loadCoverCmd(key, source, fetcher, track, m.cover.loadGen)
	}
	if m.cover.loading || m.cover.failed || m.cover.local == "" {
		return nil
	}
	if !visible {
		return nil
	}
	if m.cover.rendering {
		return nil
	}
	if m.cover.renderedAt(cols, rows, protocol) {
		return nil
	}
	m.clearCoverRender()
	m.cover.rendering = true
	m.cover.renderGen++
	return renderCoverCmd(key, m.cover.local, cols, rows, protocol, 0, m.cover.renderGen)
}

func (c coverState) renderedAt(cols, rows int, protocol string) bool {
	ready := c.block != ""
	if protocol == "kitty" {
		ready = c.imageID != 0
	}
	return ready && c.cols == cols && c.rows == rows && c.protocol == protocol
}

func (m Model) coverGeometry() (cols, rows int, protocol string, visible bool) {
	if !m.showMetadata || !m.layout.twoColumn || m.coverArtMode() == "off" || m.quitting || m.headless || m.fullVis || m.usesSimplifiedLayout() ||
		m.usesContentFirstLayout() || m.visualizerDisabled() || m.layout.tier != layoutFull {
		return 0, 0, "", false
	}
	graphics := kittyimg.SupportsGraphics()
	rows = m.layout.coverRows
	if rows < coverMinRows {
		return 0, 0, "", false
	}
	if m.cover.termCols != m.width || m.cover.termRows != m.height {
		m.cover.termCols, m.cover.termRows = m.width, m.height
		m.cover.cellWidth, m.cover.cellHeight, _ = kittyimg.CellSize()
	}
	cols = squareCoverCols(rows, m.cover.cellWidth, m.cover.cellHeight)
	if m.layout.panelWidth-cols-1 < playlistMinWidth {
		return 0, 0, "", false
	}
	protocol = "blocks"
	if graphics {
		protocol = "kitty"
	}
	return cols, rows, protocol, true
}

func squareCoverCols(rows int, cellWidth, cellHeight float64) int {
	if cellWidth <= 0 || cellHeight <= 0 {
		return rows * 2
	}
	return max(1, int(math.Round(float64(rows)*cellHeight/cellWidth)))
}

func loadCoverCmd(key, source string, fetcher provider.ArtworkFetcher, track playlist.Track, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), coverLoadTimeout)
		defer cancel()
		var (
			local string
			err   error
		)
		if source != "" {
			local, err = artcache.Resolve(ctx, source)
		} else {
			if cached, ok := artcache.Lookup(key); ok {
				local = cached
			} else {
				var raw []byte
				raw, err = fetcher.FetchArtwork(ctx, track)
				if err == nil {
					local, err = artcache.StoreForKey(key, raw)
				}
			}
		}
		return coverLoadedMsg{key: key, local: local, gen: gen, err: err}
	}
}

func renderCoverCmd(key, source string, cols, rows int, protocol string, reuseID int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		msg := coverRenderedMsg{key: key, gen: gen, cols: cols, rows: rows, protocol: protocol}
		if protocol == "kitty" {
			placement, err := kittyimg.Show(source, cols, rows, reuseID)
			msg.err = err
			msg.imageID = placement.ID
			return msg
		}
		msg.block, msg.err = kittyimg.Blocks(source, cols, rows)
		return msg
	}
}

func (m *Model) handleCoverLoaded(msg coverLoadedMsg) {
	if msg.gen != m.cover.loadGen || msg.key != m.cover.key {
		return
	}
	m.cover.loading = false
	if msg.err != nil {
		m.cover.failed = true
		applog.Debug("cover art load failed: %v", msg.err)
		return
	}
	m.cover.local = msg.local
}

func (m *Model) handleCoverRendered(msg coverRenderedMsg) {
	if msg.gen != m.cover.renderGen || msg.key != m.cover.key {
		if msg.imageID != 0 {
			kittyimg.Clear(msg.imageID)
		}
		return
	}
	m.cover.rendering = false
	if msg.err != nil {
		m.cover.failed = true
		applog.Debug("cover art render failed: %v", msg.err)
		return
	}
	m.cover.cols = msg.cols
	m.cover.rows = msg.rows
	m.cover.protocol = msg.protocol
	m.cover.block = msg.block
	if msg.imageID != 0 {
		m.cover.imageID = msg.imageID
	}
}

func (m *Model) clearCoverRender() {
	if m.cover.imageID == 0 && m.cover.block == "" && m.cover.protocol == "" &&
		m.cover.cols == 0 && m.cover.rows == 0 && !m.cover.rendering {
		return
	}
	if m.cover.imageID != 0 && m.cover.protocol == "kitty" {
		kittyimg.Clear(m.cover.imageID)
	}
	m.cover.imageID = 0
	m.cover.block = ""
	m.cover.protocol = ""
	m.cover.cols = 0
	m.cover.rows = 0
	m.cover.rendering = false
	// Reject an in-flight render whose dimensions no longer apply.
	m.cover.renderGen++
}

func (m *Model) resetCover() {
	if m.cover.key == "" && m.cover.local == "" && !m.cover.failed && !m.cover.loading &&
		m.cover.block == "" && m.cover.imageID == 0 {
		return
	}
	m.clearCoverRender()
	m.cover.key = ""
	m.cover.local = ""
	m.cover.failed = false
	m.cover.loading = false
	m.cover.loadGen++
}

func (m Model) renderCover() string {
	if m.cover.protocol == "kitty" && m.cover.imageID != 0 {
		if placement, ok := kittyimg.PlacementByID(m.cover.imageID); ok {
			return placement.Block()
		}
		return ""
	}
	return m.cover.block
}
