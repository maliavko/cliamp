package model

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bjarneo/cliamp/internal/cast"
	"github.com/bjarneo/cliamp/player"
	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

// castDiscoverTimeout is how long the device picker listens for Cast
// devices. They answer within a second or two.
const castDiscoverTimeout = 2 * time.Second

// castState is playback on a Cast device: the engine that drives it, and the
// local engine it replaced, which takes over again when playback comes back.
type castState struct {
	player *cast.Player
	local  player.Engine
}

// castMuteTimeout gives the volume back when the seek of a track that came
// back from a Cast device never lands.
const castMuteTimeout = 8 * time.Second

// castMute silences the local engine while a track that came back from a
// Cast device seeks to where the device was: a provider stream cannot seek
// before it starts, so it plays from 0 for a moment first.
type castMute struct {
	active bool
	volume float64
	volMin float64
	target time.Duration
	until  time.Time
}

// castDevicesMsg carries the Cast devices found on the local network.
type castDevicesMsg struct {
	devices []cast.Device
	err     error
}

func discoverCastCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), castDiscoverTimeout)
		defer cancel()
		devices, err := cast.Discover(ctx)
		return castDevicesMsg{devices: devices, err: err}
	}
}

func (m *Model) handleCastDevices(msg castDevicesMsg) {
	m.devicePicker.castSearching = false
	m.devicePicker.castDevices = msg.devices
	if msg.err != nil {
		m.devicePicker.castErr = msg.err.Error()
	}
	m.deviceMaybeAdjustScroll(m.effectivePlaylistVisible())
}

// casting reports whether playback is on a Cast device.
func (m Model) casting() bool { return m.cast.player != nil }

// castDeviceName names the Cast device that plays, or "".
func (m Model) castDeviceName() string {
	if m.cast.player == nil {
		return ""
	}
	return m.cast.player.Device().Name
}

// canCast reports whether a provider can play track on a Cast device.
func (m *Model) canCast(track playlist.Track) bool {
	loader, _ := findCapable(m, func(l provider.CastLoader) bool { return l.CanCast(track) })
	return loader != nil
}

// castResolver resolves tracks for the Cast engine. It keeps its own list of
// the providers that cast, so the engine can call it off the UI goroutine.
func (m *Model) castResolver() cast.Resolver {
	var loaders []provider.CastLoader
	for _, entry := range m.providers {
		if l, ok := entry.Provider.(provider.CastLoader); ok {
			loaders = append(loaders, l)
		}
	}
	return func(ctx context.Context, path string) (string, map[string]any, error) {
		track := playlist.Track{Path: path}
		for _, l := range loaders {
			if l.CanCast(track) {
				media, err := l.CastMedia(ctx, track)
				return media.AppID, media.Media, err
			}
		}
		return "", nil, fmt.Errorf("this track can't play on a Cast device; press d to play it here")
	}
}

// castTo moves playback to device. The track that plays goes on there from
// the same position; with nothing playing, the next track starts there.
func (m *Model) castTo(device cast.Device) tea.Cmd {
	if m.casting() && m.cast.player.Device().ID == device.ID {
		return nil
	}
	track, idx := m.currentPlaybackTrack()
	playing := idx >= 0 && m.player.IsPlaying()
	if playing && !m.canCast(track) {
		m.status.Warningf(statusTTLDefault, "%s can't play on %s: only Plex tracks can.", track.DisplayName(), device.Name)
		return nil
	}
	position := m.player.Position()

	next := cast.NewPlayer(device, m.castResolver())
	next.SetVolumeMin(m.player.VolumeMin())
	if m.casting() {
		old := m.cast.player
		go old.Close()
	} else {
		m.cast.local = m.player
		m.player.Stop()
	}
	m.cast.player = next
	m.player = next
	m.status.Showf(statusTTLDefault, "Playing on %s", device.Name)
	if !playing {
		return nil
	}
	return m.restartOnEngine(track, position)
}

// castBack brings playback back to this device, from where the Cast device
// was. It reports whether there was a Cast session to end.
func (m *Model) castBack() (tea.Cmd, bool) {
	if !m.casting() {
		return nil, false
	}
	track, idx := m.currentPlaybackTrack()
	playing := idx >= 0 && m.player.IsPlaying()
	position := m.player.Position()
	m.releaseCast()
	m.status.Show("Playing on this device", statusTTLDefault)
	if !playing {
		return nil, true
	}
	if position > 0 {
		m.castMute = castMute{
			active: true, volume: m.player.Volume(), volMin: m.player.VolumeMin(),
			target: position, until: time.Now().Add(castMuteTimeout),
		}
		m.player.SetVolumeMin(-90)
		m.player.SetVolume(-90)
	}
	return m.restartOnEngine(track, position), true
}

// uncastFor brings playback back to this device before track starts, when
// the Cast device cannot play it, such as a local file during a Plex cast.
func (m *Model) uncastFor(track playlist.Track) {
	if !m.casting() || m.canCast(track) {
		return
	}
	name := m.castDeviceName()
	m.releaseCast()
	m.status.Showf(statusTTLDefault, "%s can't play on %s, so it plays here.", track.DisplayName(), name)
}

// tickCastLeft stops playback when the Cast device ended the session, as when
// someone closes the app on the TV, and goes back to this device. Playing the
// track again would take the TV back from whoever closed it.
func (m *Model) tickCastLeft() {
	if !m.casting() || !m.cast.player.Left() {
		return
	}
	name := m.castDeviceName()
	m.stopPlayback()
	m.releaseCast()
	m.status.Warningf(statusTTLLong, "%s ended the session. Playback stopped.", name)
}

// tickCastMute gives the local engine its volume back once the track that
// came back from a Cast device has seeked to where the device was.
func (m *Model) tickCastMute(now time.Time) {
	mute := m.castMute
	if !mute.active {
		return
	}
	seeked := !m.buffering && m.resume.path == "" && m.player.Position() >= mute.target-time.Second
	if !seeked && now.Before(mute.until) {
		return
	}
	m.player.SetVolumeMin(mute.volMin)
	m.player.SetVolume(mute.volume)
	m.castMute = castMute{}
}

// releaseCast lets the Cast device go, which goes back to its home screen,
// and gives playback back to the local engine.
func (m *Model) releaseCast() {
	remote := m.cast.player
	go remote.Close()
	m.player = m.cast.local
	m.cast = castState{}
}

// restartOnEngine starts track on the engine that m.player now holds, at
// position. It skips the history and the start reports of playTrack, since
// the same listen goes on.
func (m *Model) restartOnEngine(track playlist.Track, position time.Duration) tea.Cmd {
	m.buffering = true
	m.bufferingAt = time.Now()
	m.err = nil
	dur := time.Duration(track.DurationSecs) * time.Second
	// A stream that cannot seek before it starts plays from 0; the resume
	// hint seeks it once it plays, as after a restart.
	m.resume.path, m.resume.secs = track.Path, int(position.Seconds())
	gen := nextRequest(&m.requests.stream)
	m.player.SetPlaybackGeneration(gen)
	return playStreamCmd(m.player, track.Path, dur, func() time.Duration { return position }, gen)
}
