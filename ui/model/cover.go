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

// coverSlot names which cover a load or render belongs to: the playing track
// beside the visualizer, or the highlighted track in the info overlay.
type coverSlot int

const (
	coverSlotPlaying coverSlot = iota
	coverSlotInfo
	coverSlotList
)

// listCoverDelay is how long the provider browser cursor rests on an entry
// before its cover loads, so scrolling a long list sends no requests.
const listCoverDelay = 200 * time.Millisecond

// listCoverDueMsg starts the cover load of a browser entry once the cursor
// has rested on it. gen ties it to the startLoad that scheduled it.
type listCoverDueMsg struct {
	key string
	gen uint64
}

type coverState struct {
	mode string

	key       string
	local     string
	aspect    float64 // image width / height, 0 when unknown
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
	slot   coverSlot
	key    string
	local  string
	aspect float64
	gen    uint64
	err    error
}

type coverRenderedMsg struct {
	slot     coverSlot
	key      string
	gen      uint64
	cols     int
	rows     int
	protocol string
	block    string
	imageID  int
	err      error
}

// SetCoverArtMode sets cover rendering to auto, on, or off. Auto draws covers
// only with Kitty graphics; on also uses ANSI half blocks elsewhere.
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

// coverEnabled reports whether covers are drawn at all. Auto draws them only
// in terminals with real image support; the ANSI half-block fallback is too
// blurry to read, so it needs an explicit "on".
func (m Model) coverEnabled() bool {
	switch m.coverArtMode() {
	case "on":
		return true
	case "auto":
		return kittyimg.SupportsGraphics()
	}
	return false
}

// artworkSource finds where track's cover comes from: a URL the cache can
// resolve, or a provider that fetches authenticated bytes. The key names the
// cover in the cache and in coverState.
func (m *Model) artworkSource(track playlist.Track) (key, source string, fetcher provider.ArtworkFetcher, ok bool) {
	source = m.baseArtworkURL(track)
	if source == "" {
		fetcher, _ = findCapable(m, func(candidate provider.ArtworkFetcher) bool {
			return candidate.CanFetchArtwork(track)
		})
	}
	if source == "" && fetcher == nil {
		return "", "", nil, false
	}
	key = track.Path + "\x00" + source
	if fetcher != nil {
		key = track.Path + "\x00provider"
	}
	return key, source, fetcher, true
}

// ensureCover schedules at most one artwork load or render. Artwork is loaded
// even while the TUI pane is hidden so media controls and desktop notifications
// can reuse the same local cache. Update calls it after applying layout and
// playback changes, so commands never block the UI.
func (m *Model) ensureCover() tea.Cmd {
	cols, rows, protocol, visible := m.coverGeometry()
	if !visible {
		m.cover.clearRender()
	}
	track, _ := m.currentPlaybackTrack()
	if m.player == nil || (!m.player.IsPlaying() && !m.buffering) || track.Path == "" {
		m.cover.reset()
		return nil
	}

	key, source, fetcher, ok := m.artworkSource(track)
	if !ok {
		m.cover.reset()
		return nil
	}
	if key != m.cover.key {
		m.cover.startLoad(key)
		return loadCoverCmd(coverSlotPlaying, key, source, trackArtwork(fetcher, track), m.cover.loadGen)
	}
	if !visible {
		return nil
	}
	return m.cover.renderCmd(coverSlotPlaying, cols, rows, protocol)
}

// ensureInfoCover loads and renders the highlighted track's cover while the
// info overlay is open, and drops it when the overlay closes. A track that is
// already playing reuses the loaded cover instead of fetching it again.
func (m *Model) ensureInfoCover() tea.Cmd {
	c := &m.infoCover
	if !m.info.visible || !m.coverEnabled() || m.headless || m.quitting {
		c.reset()
		return nil
	}
	track := m.selectedMetadataTrack()
	if track.Path == "" {
		c.reset()
		return nil
	}
	key, source, fetcher, ok := m.artworkSource(track)
	if !ok {
		c.reset()
		return nil
	}
	if key != c.key {
		c.startLoad(key)
		if key == m.cover.key && m.cover.local != "" && m.cover.aspect > 0 {
			c.loading = false
			c.local = m.cover.local
			c.aspect = m.cover.aspect
		} else {
			return loadCoverCmd(coverSlotInfo, key, source, trackArtwork(fetcher, track), c.loadGen)
		}
	}
	cols, rows, protocol, visible := m.infoCoverGeometry()
	if !visible {
		c.clearRender()
		return nil
	}
	return c.renderCmd(coverSlotInfo, cols, rows, protocol)
}

// startLoad forgets the previous cover and marks key as loading.
func (c *coverState) startLoad(key string) {
	c.clearRender()
	c.key = key
	c.local = ""
	c.aspect = 0
	c.failed = false
	c.loading = true
	c.loadGen++
}

// renderCmd starts a render at the given size unless one is running or the
// current render already matches.
func (c *coverState) renderCmd(slot coverSlot, cols, rows int, protocol string) tea.Cmd {
	if c.loading || c.failed || c.local == "" || c.rendering || c.renderedAt(cols, rows, protocol) {
		return nil
	}
	c.clearRender()
	c.rendering = true
	c.renderGen++
	return renderCoverCmd(slot, c.key, c.local, cols, rows, protocol, 0, c.renderGen)
}

func (c coverState) renderedAt(cols, rows int, protocol string) bool {
	ready := c.block != ""
	if protocol == "kitty" {
		ready = c.imageID != 0
	}
	return ready && c.cols == cols && c.rows == rows && c.protocol == protocol
}

func (m Model) coverGeometry() (cols, rows int, protocol string, visible bool) {
	if m.fullVis {
		return m.fullVisCoverGeometry()
	}
	if !m.showMetadata || !m.layout.twoColumn || !m.coverEnabled() || m.quitting || m.headless || m.usesSimplifiedLayout() ||
		m.usesContentFirstLayout() || m.visualizerDisabled() || m.layout.tier != layoutFull {
		return 0, 0, "", false
	}
	rows = m.layout.coverRows
	if rows < coverMinRows {
		return 0, 0, "", false
	}
	cellWidth, cellHeight := m.cellSize()
	cols = squareCoverCols(rows, cellWidth, cellHeight)
	if m.layout.panelWidth-cols-1 < playlistMinWidth {
		return 0, 0, "", false
	}
	return cols, rows, coverProtocol(), true
}

// fullVisCoverTop is the first frame line of the full-screen cover: the top
// of the visualizer, one blank line below the time status, as in the main
// view. Its right edge lines up with the right-aligned playback status.
const fullVisCoverTop = 3

// fullVisCoverGeometry sizes the cover in the full-screen visualizer the same
// as the main view's pane. It shares the Ctrl+I preference (show_metadata)
// with the main view.
func (m Model) fullVisCoverGeometry() (cols, rows int, protocol string, visible bool) {
	if !m.showMetadata || !m.coverEnabled() || m.quitting || m.headless || m.visualizerDisabled() {
		return 0, 0, "", false
	}
	rows = min(m.layout.mainCoverRows, m.layout.fullVisualizerRows)
	if rows < coverMinRows {
		return 0, 0, "", false
	}
	cellWidth, cellHeight := m.cellSize()
	cols = squareCoverCols(rows, cellWidth, cellHeight)
	if m.layout.panelWidth-cols-1 < playlistMinWidth {
		return 0, 0, "", false
	}
	return cols, rows, coverProtocol(), true
}

// cellSize returns the terminal cell size in pixels, cached per terminal size.
func (m Model) cellSize() (width, height float64) {
	if m.cover.termCols != m.width || m.cover.termRows != m.height {
		m.cover.termCols, m.cover.termRows = m.width, m.height
		m.cover.cellWidth, m.cover.cellHeight, _ = kittyimg.CellSize()
	}
	return m.cover.cellWidth, m.cover.cellHeight
}

func coverProtocol() string {
	if kittyimg.SupportsGraphics() {
		return "kitty"
	}
	return "blocks"
}

func squareCoverCols(rows int, cellWidth, cellHeight float64) int {
	return coverCols(rows, 1, cellWidth, cellHeight)
}

// coverCols is the width in cells of a rows-tall box with the image aspect
// (width / height). Without a known cell size it assumes 1:2 cells.
func coverCols(rows int, aspect, cellWidth, cellHeight float64) int {
	if cellWidth <= 0 || cellHeight <= 0 {
		cellWidth, cellHeight = 1, 2
	}
	return max(1, int(math.Round(float64(rows)*aspect*cellHeight/cellWidth)))
}

// Info overlay layout: the cover sits after a left margin, then a gap, then
// the field text with its usual two-space indent.
const (
	infoCoverMargin = 2
	infoCoverGap    = 1
	infoMinText     = 30
)

// infoCoverGeometry sizes the info-overlay cover. Its height is the number of
// field lines, capped by the body; its width keeps the image aspect. Fields
// wrap in the width the cover leaves, which can add lines and so grow the
// cover; the loop stops once the height settles. Height only grows and is
// capped by the body, so it settles within a few passes.
func (m Model) infoCoverGeometry() (cols, rows int, protocol string, visible bool) {
	c := m.infoCover
	if !m.info.visible || c.local == "" || c.aspect <= 0 {
		return 0, 0, "", false
	}
	budget := m.effectivePlaylistVisible()
	width := m.infoWidth()
	cellWidth, cellHeight := m.cellSize()
	rows = min(budget, len(m.infoLinesAt(width)))
	for {
		cols = coverCols(rows, c.aspect, cellWidth, cellHeight)
		textWidth := width - infoCoverMargin - cols - infoCoverGap
		if rows < coverMinRows || cols > width/2 || textWidth < infoMinText {
			return 0, 0, "", false
		}
		next := min(budget, len(m.infoLinesAt(textWidth)))
		if next == rows {
			return cols, rows, coverProtocol(), true
		}
		rows = next
	}
}

// infoTextWidth is the width left for the field text beside the cover.
func (m Model) infoTextWidth() int {
	if cols, _, _, visible := m.infoCoverGeometry(); visible {
		return m.infoWidth() - infoCoverMargin - cols - infoCoverGap
	}
	return m.infoWidth()
}

// trackArtwork returns the authenticated download of track's cover, or nil
// when the cover comes from a URL instead.
func trackArtwork(fetcher provider.ArtworkFetcher, track playlist.Track) func(context.Context) ([]byte, error) {
	if fetcher == nil {
		return nil
	}
	return func(ctx context.Context) ([]byte, error) { return fetcher.FetchArtwork(ctx, track) }
}

// loadCoverCmd resolves source through the cache, or, without a source,
// downloads the cover with fetch and caches it under key.
func loadCoverCmd(slot coverSlot, key, source string, fetch func(context.Context) ([]byte, error), gen uint64) tea.Cmd {
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
				raw, err = fetch(ctx)
				if err == nil {
					local, err = artcache.StoreForKey(key, raw)
				}
			}
		}
		msg := coverLoadedMsg{slot: slot, key: key, local: local, gen: gen, err: err}
		if err == nil {
			// The aspect only sizes the info-overlay cover; a failure keeps
			// the cover usable for the square pane and notifications.
			msg.aspect, _ = kittyimg.Aspect(local)
		}
		return msg
	}
}

func renderCoverCmd(slot coverSlot, key, source string, cols, rows int, protocol string, reuseID int, gen uint64) tea.Cmd {
	return func() tea.Msg {
		msg := coverRenderedMsg{slot: slot, key: key, gen: gen, cols: cols, rows: rows, protocol: protocol}
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

func (m *Model) coverFor(slot coverSlot) *coverState {
	switch slot {
	case coverSlotInfo:
		return &m.infoCover
	case coverSlotList:
		return &m.listCover
	}
	return &m.cover
}

// selectedBrowserList returns the provider browser entry under the cursor,
// in the filtered results while a filter is active.
func (m Model) selectedBrowserList() (playlist.PlaylistInfo, bool) {
	if m.provPane.loading {
		return playlist.PlaylistInfo{}, false
	}
	index := m.provPane.cursor
	if m.provSearch.active {
		if m.provSearch.cursor < 0 || m.provSearch.cursor >= len(m.provSearch.results) {
			return playlist.PlaylistInfo{}, false
		}
		index = m.provSearch.results[m.provSearch.cursor]
	}
	if index < 0 || index >= len(m.provPane.lists) {
		return playlist.PlaylistInfo{}, false
	}
	return m.provPane.lists[index], true
}

// listCoverSource returns the provider browser entry under the cursor and the
// provider that serves its cover, when the browser column shows a cover.
func (m *Model) listCoverSource() (playlist.PlaylistInfo, provider.ListArtworkFetcher, bool) {
	if !m.listColumnShowsCover() || m.quitting {
		return playlist.PlaylistInfo{}, nil, false
	}
	list, ok := m.selectedBrowserList()
	if !ok {
		return playlist.PlaylistInfo{}, nil, false
	}
	fetcher := m.provider.(provider.ListArtworkFetcher)
	if !fetcher.CanFetchListArtwork(list) {
		return playlist.PlaylistInfo{}, nil, false
	}
	return list, fetcher, true
}

func listCoverKey(providerName string, list playlist.PlaylistInfo) string {
	return "list\x00" + providerName + "\x00" + list.ID
}

// ensureListCover shows the cover of the provider browser entry under the
// cursor. A new entry waits listCoverDelay before it loads; leaving the
// browser drops the cover.
func (m *Model) ensureListCover() tea.Cmd {
	c := &m.listCover
	list, _, ok := m.listCoverSource()
	if !ok {
		c.reset()
		return nil
	}
	key := listCoverKey(m.provider.Name(), list)
	if key != c.key {
		c.startLoad(key)
		gen := c.loadGen
		return tea.Tick(listCoverDelay, func(time.Time) tea.Msg { return listCoverDueMsg{key: key, gen: gen} })
	}
	cols, rows, protocol, visible := m.listCoverGeometry()
	if !visible {
		c.clearRender()
		return nil
	}
	return c.renderCmd(coverSlotList, cols, rows, protocol)
}

// handleListCoverDue loads the entry cover if the cursor is still on it.
func (m *Model) handleListCoverDue(msg listCoverDueMsg) tea.Cmd {
	c := &m.listCover
	if msg.key != c.key || msg.gen != c.loadGen || !c.loading {
		return nil
	}
	list, fetcher, ok := m.listCoverSource()
	if !ok || listCoverKey(m.provider.Name(), list) != msg.key {
		return nil
	}
	fetch := func(ctx context.Context) ([]byte, error) { return fetcher.FetchListArtwork(ctx, list) }
	return loadCoverCmd(coverSlotList, msg.key, "", fetch, msg.gen)
}

// listCoverTop is the first list body line of the provider browser cover.
// The column has its own header, as the settings pane does, so the cover
// starts right under it; the entry's description follows the cover.
const listCoverTop = 0

// listColumn is the provider browser column right of the list.
type listColumn struct {
	listWidth int // width of the list left of the gutter
	width     int // column width, as the main view's settings pane
	coverCols int // 0 when the column has no room for a cover
	coverRows int
}

// listColumnGeometry sizes the provider browser column like the main view's
// settings pane, so the list keeps one width while the cursor moves. Its
// cover has the main view's cover size, shrunk to the column width. It shows
// with covers on (Ctrl+I) for providers that describe or picture their
// entries.
func (m Model) listColumnGeometry() (listColumn, bool) {
	if m.focus != focusProvider || m.activeScreen() != screenMain || !m.showMetadata || m.headless || m.provider == nil {
		return listColumn{}, false
	}
	_, art := m.provider.(provider.ListArtworkFetcher)
	_, details := m.provider.(provider.ListDetailer)
	if !art && !details {
		return listColumn{}, false
	}
	c := listColumn{width: sideColumnWidth(m.layout.panelWidth)}
	c.listWidth = m.layout.panelWidth - columnGutterWidth - c.width
	if c.listWidth < playlistMinWidth || m.effectivePlaylistVisible() <= listCoverTop {
		return listColumn{}, false
	}
	cellWidth, cellHeight := m.cellSize()
	rows := min(m.layout.mainCoverRows, m.effectivePlaylistVisible()-listCoverTop)
	for rows >= coverMinRows && squareCoverCols(rows, cellWidth, cellHeight) > c.width {
		rows--
	}
	if rows >= coverMinRows {
		c.coverRows = rows
		c.coverCols = squareCoverCols(rows, cellWidth, cellHeight)
	}
	return c, true
}

// listColumnShowsCover reports whether the browser column reserves a cover.
func (m Model) listColumnShowsCover() bool {
	c, ok := m.listColumnGeometry()
	if !ok || c.coverRows == 0 || !m.coverEnabled() {
		return false
	}
	_, art := m.provider.(provider.ListArtworkFetcher)
	return art
}

// listCoverGeometry sizes the browser cover from the column.
func (m Model) listCoverGeometry() (cols, rows int, protocol string, visible bool) {
	if m.listCover.local == "" || !m.listColumnShowsCover() {
		return 0, 0, "", false
	}
	c, _ := m.listColumnGeometry()
	return c.coverCols, c.coverRows, coverProtocol(), true
}

func (m *Model) handleCoverLoaded(msg coverLoadedMsg) {
	c := m.coverFor(msg.slot)
	if msg.gen != c.loadGen || msg.key != c.key {
		return
	}
	c.loading = false
	if msg.err != nil {
		c.failed = true
		applog.Debug("cover art load failed: %v", msg.err)
		return
	}
	c.local = msg.local
	c.aspect = msg.aspect
}

func (m *Model) handleCoverRendered(msg coverRenderedMsg) {
	c := m.coverFor(msg.slot)
	if msg.gen != c.renderGen || msg.key != c.key {
		if msg.imageID != 0 {
			kittyimg.Clear(msg.imageID)
		}
		return
	}
	c.rendering = false
	if msg.err != nil {
		c.failed = true
		applog.Debug("cover art render failed: %v", msg.err)
		return
	}
	c.cols = msg.cols
	c.rows = msg.rows
	c.protocol = msg.protocol
	c.block = msg.block
	if msg.imageID != 0 {
		c.imageID = msg.imageID
	}
}

func (m *Model) clearCoverRender() { m.cover.clearRender() }

func (c *coverState) clearRender() {
	if c.imageID == 0 && c.block == "" && c.protocol == "" &&
		c.cols == 0 && c.rows == 0 && !c.rendering {
		return
	}
	if c.imageID != 0 && c.protocol == "kitty" {
		kittyimg.Clear(c.imageID)
	}
	c.imageID = 0
	c.block = ""
	c.protocol = ""
	c.cols = 0
	c.rows = 0
	c.rendering = false
	// Reject an in-flight render whose dimensions no longer apply.
	c.renderGen++
}

func (m *Model) resetCover() { m.cover.reset() }

func (c *coverState) reset() {
	if c.key == "" && c.local == "" && !c.failed && !c.loading &&
		c.block == "" && c.imageID == 0 {
		return
	}
	c.clearRender()
	c.key = ""
	c.local = ""
	c.aspect = 0
	c.failed = false
	c.loading = false
	c.loadGen++
}

func (m Model) renderCover() string { return m.cover.render() }

func (c coverState) render() string {
	if c.protocol == "kitty" && c.imageID != 0 {
		if placement, ok := kittyimg.PlacementByID(c.imageID); ok {
			return placement.Block()
		}
		return ""
	}
	return c.block
}
