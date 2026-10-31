package plex

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bjarneo/cliamp/applog"
	"github.com/bjarneo/cliamp/config"
	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

// ID prefixes and section labels used to group Plex playlists and albums in
// the browse view. Playlist IDs are prefixed so they can be told apart from
// album ratingKeys (both live in the same server-side metadata key space).
const (
	// TrackURIPrefix is the token-free path stored in queues, history,
	// favorites, playlists, resume state, and IPC responses.
	TrackURIPrefix   = "plex:track:"
	prefixPlaylist   = "pl:"
	prefixStation    = "st:"
	sectionAlbums    = "Albums"
	sectionPlaylists = "Playlists"
	sectionRadio     = "Radio"

	// stationBatch is how many tracks a station loads at a time. When the
	// last one starts, the next batch is appended; Ctrl+R on the open
	// station starts it again for a fresh selection.
	stationBatch  = 30
	albumCacheTTL = 5 * time.Minute
)

type trackCacheEntry struct {
	tracks   []playlist.Track
	loadedAt time.Time
}

// Compile-time interface checks.
var (
	_ playlist.RefreshablePlaylist = (*Provider)(nil)
	_ provider.Searcher            = (*Provider)(nil)
	_ provider.AlbumTrackLoader    = (*Provider)(nil)
	_ provider.TrackFavoriter      = (*Provider)(nil)
	_ provider.TrackFavoriteState  = (*Provider)(nil)
	_ provider.ProgressReporter    = (*Provider)(nil)
	_ provider.ArtworkFetcher      = (*Provider)(nil)
	_ provider.ListArtworkFetcher  = (*Provider)(nil)
	_ provider.ListDetailer        = (*Provider)(nil)
	_ provider.QueueContinuer      = (*Provider)(nil)
)

// Provider implements playlist.Provider for a Plex Media Server.
// Playlists() returns server audio playlists (Section "Playlists") followed
// by albums from all music library sections (Section "Albums").
// Tracks() returns the tracks for a playlist or album ratingKey; playlist IDs
// are distinguished by the "pl:" prefix.
type Provider struct {
	client        *Client
	mu            sync.Mutex
	playlistCache []playlist.PlaylistInfo
	trackCache    map[string]trackCacheEntry
	partKeys      map[string]string
	artKeys       map[string]string
	listArtKeys   map[string]string // PlaylistInfo.ID -> artwork path
	listDetails   map[string]provider.ListDescription
	favorites     map[string]bool
	stations      []string // [plex] stations: nil shows all, empty hides all
	// stationQueues maps the path of the last track of each station batch
	// to the server play queue that continues after it.
	stationQueues map[string]stationQueue
	now           func() time.Time
}

// stationQueue is where a station's server play queue continues.
type stationQueue struct {
	queueID    int
	lastItemID int
}

// newProvider returns a Provider backed by the given Client.
func newProvider(client *Client) *Provider {
	return &Provider{client: client, now: time.Now}
}

// NewFromConfig returns a Provider from a PlexConfig, or nil if URL or Token is missing.
func NewFromConfig(cfg config.PlexConfig) *Provider {
	if !cfg.IsSet() {
		return nil
	}
	var p *Provider
	if cfg.ClientID != "" && cfg.ServerID != "" {
		p = newProvider(NewAccountClient(cfg.URL, cfg.Token, cfg.ClientID, cfg.ServerID, cfg.Libraries...))
	} else {
		p = newProvider(NewClient(cfg.URL, cfg.Token, cfg.Libraries...))
	}
	p.stations = cfg.Stations
	return p
}

// Name returns the display name used in the provider selector.
func (p *Provider) Name() string { return "Plex" }

// Playlists returns the server's audio playlists first, then all albums
// across all Plex music library sections, each tagged with its section so the
// browse view renders them under headers. Results are cached after the first
// successful call.
func (p *Provider) Playlists() ([]playlist.PlaylistInfo, error) {
	p.mu.Lock()
	if p.playlistCache != nil {
		cached := slices.Clone(p.playlistCache)
		p.mu.Unlock()
		return cached, nil
	}
	p.mu.Unlock()

	var lists []playlist.PlaylistInfo
	listArt := make(map[string]string)
	details := make(map[string]provider.ListDescription)

	serverPlaylists, err := p.client.Playlists()
	if err != nil {
		return nil, fmt.Errorf("plex: list playlists: %w", err)
	}
	for _, pl := range serverPlaylists {
		if pl.Thumb != "" {
			listArt[prefixPlaylist+pl.RatingKey] = pl.Thumb
		}
		details[prefixPlaylist+pl.RatingKey] = playlistDescription(pl)
		lists = append(lists, playlist.PlaylistInfo{
			ID:           prefixPlaylist + pl.RatingKey,
			Name:         pl.Title,
			TrackCount:   pl.TrackCount,
			DurationSecs: pl.DurationSecs,
			Section:      sectionPlaylists,
		})
	}

	sections, err := p.client.MusicSections()
	if err != nil {
		return nil, err
	}
	// Playlists, then radio stations, then albums.
	lists = append(lists, p.stationLists(sections, details)...)
	for _, sec := range sections {
		albums, err := p.client.Albums(sec.Key)
		if err != nil {
			return nil, err
		}
		for _, a := range albums {
			name := a.ArtistName + " — " + a.Title
			if a.Year > 0 {
				name = fmt.Sprintf("%s — %s (%d)", a.ArtistName, a.Title, a.Year)
			}
			if a.Thumb != "" {
				listArt[a.RatingKey] = a.Thumb
			}
			details[a.RatingKey] = albumDescription(a)
			lists = append(lists, playlist.PlaylistInfo{
				ID:         a.RatingKey,
				Name:       name,
				TrackCount: a.TrackCount,
				Section:    sectionAlbums,
			})
		}
	}

	if len(lists) == 0 {
		return nil, fmt.Errorf("plex: no matching music libraries or audio playlists found on this server")
	}

	p.mu.Lock()
	p.playlistCache = lists
	p.listArtKeys = listArt
	p.listDetails = details
	p.mu.Unlock()

	return slices.Clone(lists), nil
}

// Refresh clears cached playlist and track data so the next call re-fetches
// from the server. Implements playlist.Refresher.
func (p *Provider) Refresh() {
	p.mu.Lock()
	p.playlistCache = nil
	p.listArtKeys = nil
	p.listDetails = nil
	p.trackCache = nil
	p.partKeys = nil
	p.artKeys = nil
	p.favorites = nil
	p.mu.Unlock()
	p.client.RefreshConnection()
}

// CanRefreshPlaylist reports whether id is a stable Plex album or playlist
// ratingKey that can be reloaded in place after Refresh.
func (p *Provider) CanRefreshPlaylist(id string) bool {
	id = strings.TrimPrefix(id, prefixPlaylist)
	return validRatingKey(id)
}

// Tracks returns the tracks for the playlist or album identified by
// playlistID. Playlist IDs (prefixed "pl:") load via the server playlists
// endpoint; album IDs load via the album children endpoint. Tracks with no
// ratingKey or streamable part are silently skipped. Track paths are token-free
// logical URIs; ResolveSource creates the authenticated URL only when playback
// starts. Album tracks are cached per album ID; playlist tracks are
// intentionally not cached so smart playlists always reflect current server
// contents.
func (p *Provider) Tracks(playlistID string) ([]playlist.Track, error) {
	if key, ok := strings.CutPrefix(playlistID, prefixStation); ok {
		// A station is a new play queue each time, never a cached list.
		page, err := p.client.StationTracks(key, stationBatch)
		if err != nil {
			return nil, err
		}
		return p.stationBatchTracks(page), nil
	}
	isPlaylist := strings.HasPrefix(playlistID, prefixPlaylist)

	p.mu.Lock()
	if !isPlaylist && p.trackCache != nil {
		if cached, ok := p.trackCache[playlistID]; ok && p.now().Sub(cached.loadedAt) < albumCacheTTL {
			p.mu.Unlock()
			return slices.Clone(cached.tracks), nil
		}
		delete(p.trackCache, playlistID)
	}
	p.mu.Unlock()

	var (
		plexTracks []Track
		err        error
	)
	if isPlaylist {
		plexTracks, err = p.client.PlaylistTracks(strings.TrimPrefix(playlistID, prefixPlaylist))
	} else {
		plexTracks, err = p.client.Tracks(playlistID)
	}
	if err != nil {
		return nil, err
	}

	tracks := p.convertTracks(plexTracks, 0)

	if !isPlaylist {
		p.mu.Lock()
		if p.trackCache == nil {
			p.trackCache = make(map[string]trackCacheEntry)
		}
		p.trackCache[playlistID] = trackCacheEntry{tracks: tracks, loadedAt: p.now()}
		p.mu.Unlock()
	}

	return slices.Clone(tracks), nil
}

// SearchTracks searches the Plex music library for tracks matching query.
// Implements provider.Searcher.
func (p *Provider) SearchTracks(_ context.Context, query string, limit int) ([]playlist.Track, error) {
	plexTracks, err := p.client.Search(query)
	if err != nil {
		return nil, err
	}
	return p.convertTracks(plexTracks, limit), nil
}

// convertTracks converts Plex tracks to playlist tracks, skipping entries
// without a streamable part. If limit > 0, at most limit tracks are returned.
func (p *Provider) convertTracks(plexTracks []Track, limit int) []playlist.Track {
	tracks := make([]playlist.Track, 0, len(plexTracks))
	for _, t := range plexTracks {
		if t.RatingKey == "" || t.PartKey == "" {
			continue
		}
		p.mu.Lock()
		if p.partKeys == nil {
			p.partKeys = make(map[string]string)
		}
		p.partKeys[t.RatingKey] = t.PartKey
		if t.Thumb != "" {
			if p.artKeys == nil {
				p.artKeys = make(map[string]string)
			}
			p.artKeys[t.RatingKey] = t.Thumb
		}
		if p.favorites == nil {
			p.favorites = make(map[string]bool)
		}
		p.favorites[t.RatingKey] = t.UserRating >= 10
		p.mu.Unlock()
		tracks = append(tracks, playlist.Track{
			Path:         TrackURIPrefix + t.RatingKey,
			Title:        t.Title,
			Artist:       t.ArtistName,
			Album:        t.AlbumName,
			Year:         t.Year,
			TrackNumber:  t.TrackNumber,
			DurationSecs: t.Duration / 1000,
			Stream:       true,
			ProviderMeta: map[string]string{provider.MetaPlexID: t.RatingKey},
		})
		if limit > 0 && len(tracks) >= limit {
			break
		}
	}
	return tracks
}

// ResolveSource turns a token-free Plex track URI into an authenticated
// stream URL at play time. A restored entry fetches its part metadata when
// the in-memory mapping is absent after a restart.
func (p *Provider) ResolveSource(uri string) (string, error) {
	id, ok := trackIDFromPath(uri)
	if !ok {
		return "", fmt.Errorf("plex: invalid track uri %q", uri)
	}
	p.mu.Lock()
	partKey := p.partKeys[id]
	p.mu.Unlock()
	if partKey == "" {
		track, err := p.client.Track(id)
		if err != nil {
			return "", fmt.Errorf("plex: resolve track %s: %w", id, err)
		}
		if track.PartKey == "" {
			return "", fmt.Errorf("plex: track %s has no streamable part", id)
		}
		partKey = track.PartKey
		p.mu.Lock()
		if p.partKeys == nil {
			p.partKeys = make(map[string]string)
		}
		p.partKeys[id] = partKey
		if track.Thumb != "" {
			if p.artKeys == nil {
				p.artKeys = make(map[string]string)
			}
			p.artKeys[id] = track.Thumb
		}
		if p.favorites == nil {
			p.favorites = make(map[string]bool)
		}
		p.favorites[id] = track.UserRating >= 10
		p.mu.Unlock()
	}
	return p.client.StreamURL(partKey), nil
}

func trackIDFromPath(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, TrackURIPrefix)
	if !ok || !validRatingKey(id) {
		return "", false
	}
	return id, true
}

func validRatingKey(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// CanFavoriteTrack reports whether track came from this Plex provider.
func (p *Provider) CanFavoriteTrack(track playlist.Track) bool {
	return plexTrackID(track) != ""
}

// FavoriteState reports the rating returned with the latest Plex metadata.
// Plex is authoritative even when known is false; the UI must not fall back
// to cliamp's local favorites for this track.
func (p *Provider) FavoriteState(track playlist.Track) (favorite, known bool) {
	id := plexTrackID(track)
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == "" {
		return false, false
	}
	favorite, known = p.favorites[id]
	return favorite, known
}

// SetTrackFavorite updates the current user's favorite state in Plex.
func (p *Provider) SetTrackFavorite(ctx context.Context, track playlist.Track, favorite bool) error {
	id := plexTrackID(track)
	if id == "" {
		return fmt.Errorf("plex: track has no rating key")
	}
	rating := float64(0)
	if favorite {
		rating = 10
	}
	if err := p.client.SetRating(ctx, id, rating); err != nil {
		return err
	}
	p.mu.Lock()
	if p.favorites == nil {
		p.favorites = make(map[string]bool)
	}
	p.favorites[id] = favorite
	p.mu.Unlock()
	return nil
}

// CanFetchArtwork reports whether track belongs to Plex. A restored track may
// not have its artwork path in memory yet; FetchArtwork restores it from the
// item metadata before downloading the image.
func (p *Provider) CanFetchArtwork(track playlist.Track) bool {
	return plexTrackID(track) != ""
}

// FetchArtwork returns authenticated cover bytes without exposing a token in
// the track or URL consumed by the UI.
func (p *Provider) FetchArtwork(ctx context.Context, track playlist.Track) ([]byte, error) {
	id := plexTrackID(track)
	if id == "" {
		return nil, fmt.Errorf("plex: artwork: track has no rating key")
	}
	p.mu.Lock()
	artKey := p.artKeys[id]
	p.mu.Unlock()
	if artKey == "" {
		item, err := p.client.Track(id)
		if err != nil {
			return nil, fmt.Errorf("plex: artwork metadata %s: %w", id, err)
		}
		artKey = item.Thumb
		if artKey == "" {
			return nil, fmt.Errorf("plex: track %s has no artwork", id)
		}
		p.mu.Lock()
		if p.artKeys == nil {
			p.artKeys = make(map[string]string)
		}
		p.artKeys[id] = artKey
		p.mu.Unlock()
	}
	return p.client.Artwork(ctx, artKey)
}

// RestoreTrack claims a saved Plex track for the resume context. The path is
// already the token-free plex:track URI, and ResolveSource adds the current
// credentials when playback starts, so the track is only checked and tagged.
func (p *Provider) RestoreTrack(track playlist.Track) (playlist.Track, bool) {
	id, ok := trackIDFromPath(track.Path)
	if !ok {
		return playlist.Track{}, false
	}
	meta := make(map[string]string, len(track.ProviderMeta)+1)
	for k, v := range track.ProviderMeta {
		meta[k] = v
	}
	meta[provider.MetaPlexID] = id
	track.ProviderMeta = meta
	track.Stream = true
	return track, true
}

// stationArt is the cover of every radio station. Plex sends stations with
// only a client-drawn icon, so the browser would show no cover for them.
//
//go:embed station.png
var stationArt []byte

// CanFetchListArtwork reports whether the last Playlists call returned
// artwork for list. Radio stations always have the built-in cover. It does
// no I/O.
func (p *Provider) CanFetchListArtwork(list playlist.PlaylistInfo) bool {
	if strings.HasPrefix(list.ID, prefixStation) {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listArtKeys[list.ID] != ""
}

// FetchListArtwork downloads the cover of an album or the composite of a
// playlist with the token in a request header, or returns the built-in
// cover of a radio station.
func (p *Provider) FetchListArtwork(ctx context.Context, list playlist.PlaylistInfo) ([]byte, error) {
	if strings.HasPrefix(list.ID, prefixStation) {
		return slices.Clone(stationArt), nil
	}
	p.mu.Lock()
	path := p.listArtKeys[list.ID]
	p.mu.Unlock()
	if path == "" {
		return nil, fmt.Errorf("plex: %s has no artwork", list.ID)
	}
	return p.client.Artwork(ctx, path)
}

// stationBatchTracks converts one station batch and remembers that its last
// track continues the server play queue.
func (p *Provider) stationBatchTracks(page PlayQueuePage) []playlist.Track {
	tracks := p.convertTracks(page.Tracks, 0)
	if len(tracks) == 0 || page.LastItemID == 0 {
		return tracks
	}
	p.mu.Lock()
	if p.stationQueues == nil {
		p.stationQueues = make(map[string]stationQueue)
	}
	p.stationQueues[tracks[len(tracks)-1].Path] = stationQueue{queueID: page.QueueID, lastItemID: page.LastItemID}
	p.mu.Unlock()
	return tracks
}

// CanContinueAfter reports whether track ends a station batch. It does no I/O.
func (p *Provider) CanContinueAfter(track playlist.Track) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.stationQueues[track.Path]
	return ok
}

// ContinueAfter returns the next station batch after track, the last track
// of the batch before. No tracks means the station's queue has ended.
func (p *Provider) ContinueAfter(track playlist.Track) ([]playlist.Track, error) {
	p.mu.Lock()
	queue, ok := p.stationQueues[track.Path]
	delete(p.stationQueues, track.Path)
	p.mu.Unlock()
	if !ok {
		return nil, nil
	}
	page, err := p.client.QueueTracksAfter(queue.queueID, queue.lastItemID, stationBatch)
	if err != nil {
		// Keep the queue so a later start of the same track can retry.
		p.mu.Lock()
		if p.stationQueues == nil {
			p.stationQueues = make(map[string]stationQueue)
		}
		p.stationQueues[track.Path] = queue
		p.mu.Unlock()
		return nil, err
	}
	return p.stationBatchTracks(page), nil
}

// stationLists returns the radio stations of the music sections as browser
// entries, picked and ordered by the stations setting. A section whose
// stations cannot be read is skipped: stations are an extra, not the library.
func (p *Provider) stationLists(sections []Section, details map[string]provider.ListDescription) []playlist.PlaylistInfo {
	if p.stations != nil && len(p.stations) == 0 {
		return nil
	}
	var lists []playlist.PlaylistInfo
	for _, sec := range sections {
		stations, err := p.client.Stations(sec.Key)
		if err != nil {
			applog.Warn("plex: %v", err)
			continue
		}
		for _, st := range stations {
			name := st.Title
			if len(sections) > 1 {
				name += " · " + sec.Title
			}
			id := prefixStation + st.Key
			lists = append(lists, playlist.PlaylistInfo{ID: id, Name: name, Section: sectionRadio})
			details[id] = provider.ListDescription{
				Title:    st.Title,
				Subtitle: "Radio station",
				Lines:    []string{"Plays on without end; Ctrl+R for a fresh selection"},
			}
		}
	}
	if p.stations == nil {
		return lists
	}
	var picked []playlist.PlaylistInfo
	for _, want := range p.stations {
		for _, l := range lists {
			if strings.EqualFold(details[l.ID].Title, want) {
				picked = append(picked, l)
			}
		}
	}
	return picked
}

// ListDetails describes an album or playlist from the last Playlists call.
func (p *Provider) ListDetails(list playlist.PlaylistInfo) (provider.ListDescription, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.listDetails[list.ID]
	return d, ok
}

func albumDescription(a Album) provider.ListDescription {
	d := provider.ListDescription{Title: a.Title, Subtitle: a.ArtistName}
	released := a.Released
	if released == "" && a.Year > 0 {
		released = strconv.Itoa(a.Year)
	}
	if line := joinNonEmpty(" · ", released, a.Studio); line != "" {
		d.Lines = append(d.Lines, line)
	}
	if len(a.Genres) > 0 {
		d.Lines = append(d.Lines, strings.Join(a.Genres, ", "))
	}
	if a.TrackCount > 0 {
		d.Lines = append(d.Lines, countLabel(a.TrackCount, "track"))
	}
	if line := playedLine(a.ViewCount, a.LastViewed); line != "" {
		d.Lines = append(d.Lines, line)
	}
	return d
}

func playlistDescription(pl Playlist) provider.ListDescription {
	d := provider.ListDescription{Title: pl.Title, Subtitle: "Playlist"}
	if pl.Smart {
		d.Subtitle = "Smart playlist"
	}
	length := ""
	if pl.DurationSecs > 0 {
		length = durationLabel(pl.DurationSecs)
	}
	tracks := ""
	if pl.TrackCount > 0 {
		tracks = countLabel(pl.TrackCount, "track")
	}
	if line := joinNonEmpty(" · ", tracks, length); line != "" {
		d.Lines = append(d.Lines, line)
	}
	if line := playedLine(pl.ViewCount, pl.LastViewed); line != "" {
		d.Lines = append(d.Lines, line)
	}
	return d
}

func joinNonEmpty(sep string, parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), sep)
}

func countLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// durationLabel formats a list length as "42 min" or "3 h 05 min".
func durationLabel(secs int) string {
	minutes := (secs + 30) / 60
	if minutes < 60 {
		return strconv.Itoa(minutes) + " min"
	}
	return fmt.Sprintf("%d h %02d min", minutes/60, minutes%60)
}

// playedLine reports how often an entry played and the month it last did.
func playedLine(count int, lastViewed int64) string {
	if count <= 0 {
		return ""
	}
	line := "Played " + strconv.Itoa(count) + " times"
	if count == 1 {
		line = "Played once"
	}
	if lastViewed > 0 {
		line += " · " + time.Unix(lastViewed, 0).Format("Jan 2006")
	}
	return line
}

func plexTrackID(track playlist.Track) string {
	if id := track.Meta(provider.MetaPlexID); id != "" {
		return id
	}
	id, _ := trackIDFromPath(track.Path)
	return id
}

func plexTrackDuration(track playlist.Track) time.Duration {
	return time.Duration(track.DurationSecs) * time.Second
}

// CanReportPlayback reports whether track belongs to this Plex server.
func (p *Provider) CanReportPlayback(track playlist.Track) bool {
	return plexTrackID(track) != ""
}

// ReportNowPlaying sends the initial playing timeline update to Plex.
func (p *Provider) ReportNowPlaying(track playlist.Track, position time.Duration, _ bool) error {
	id := plexTrackID(track)
	if id == "" {
		return nil
	}
	return p.client.ReportTimeline(context.Background(), id, "playing", position, plexTrackDuration(track))
}

// ReportProgress sends an interim playing timeline update to Plex.
func (p *Provider) ReportProgress(track playlist.Track, position time.Duration) error {
	id := plexTrackID(track)
	if id == "" {
		return nil
	}
	return p.client.ReportTimeline(context.Background(), id, "playing", position, plexTrackDuration(track))
}

// ReportScrobble sends a final stopped timeline and records one completed play.
func (p *Provider) ReportScrobble(track playlist.Track, elapsed, duration time.Duration, _ bool) error {
	id := plexTrackID(track)
	if id == "" {
		return nil
	}
	if duration <= 0 {
		duration = plexTrackDuration(track)
	}
	timelineErr := p.client.ReportTimeline(context.Background(), id, "stopped", elapsed, duration)
	scrobbleErr := p.client.Scrobble(context.Background(), id)
	return errors.Join(timelineErr, scrobbleErr)
}

// AlbumTracks returns the tracks for the given album (ratingKey).
// Implements provider.AlbumTrackLoader.
func (p *Provider) AlbumTracks(albumID string) ([]playlist.Track, error) {
	return p.Tracks(albumID)
}
