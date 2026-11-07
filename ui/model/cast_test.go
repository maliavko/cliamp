package model

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bjarneo/cliamp/internal/cast"
	"github.com/bjarneo/cliamp/player"
	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

// castTestProvider casts "cast:" tracks.
type castTestProvider struct{ commandsTestProvider }

func (castTestProvider) CanCast(t playlist.Track) bool { return strings.HasPrefix(t.Path, "cast:") }
func (castTestProvider) CastMedia(context.Context, playlist.Track) (provider.CastMedia, error) {
	return provider.CastMedia{AppID: "app"}, nil
}

var tv = cast.Device{ID: "tv", Name: "SHIELD", Model: "SHIELD Android TV", Addr: netip.MustParseAddrPort("192.0.2.1:8009")}

func castTestModel() Model {
	m := keybindingTestModel()
	prov := castTestProvider{commandsTestProvider{name: "Cast"}}
	m.providers = append(m.providers, provider.Entry{Key: "cast", Name: "Cast", Provider: prov})
	m.devicePicker = devicePickerState{
		visible:     true,
		devices:     []player.AudioDevice{{Name: "speakers", Description: "Speakers", Active: true}},
		castDevices: []cast.Device{tv},
	}
	return m
}

// The picker lists the local outputs, then the Cast devices, each under its
// header, and marks where the sound plays.
func TestDevicePickerSections(t *testing.T) {
	m := castTestModel()
	m.devicePicker.cursor = 1
	lines, cursorAt := m.deviceLines()
	text := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"This device", "Speakers ●", "Cast", "SHIELD · SHIELD Android TV"} {
		if !strings.Contains(text, want) {
			t.Fatalf("picker lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(ansi.Strip(lines[cursorAt]), "SHIELD") {
		t.Fatalf("cursor line %d = %q, want the Cast device", cursorAt, ansi.Strip(lines[cursorAt]))
	}
}

// A track that no provider casts greys the Cast devices out while it plays.
func TestDevicePickerMarksUncastableTrack(t *testing.T) {
	m := castTestModel()
	m.playlist.Replace([]playlist.Track{{Path: "/music/local.mp3", Title: "Local"}})
	m.playlist.SetIndex(0)
	m.player.(*playbackFakeEngine).playing = true
	m.playingTrack, m.playingTrackActive = playlist.Track{Path: "/music/local.mp3"}, true
	lines, _ := m.deviceLines()
	if text := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(text, "SHIELD · SHIELD Android TV (Plex tracks only)") {
		t.Fatalf("Cast device not marked for a local track:\n%s", text)
	}
	if cmd := m.castTo(tv); cmd != nil || m.casting() {
		t.Fatal("castTo moved a track that can't cast")
	}
}

// Choosing the Cast device hands the engine over, and choosing the local
// output brings the local engine back.
func TestCastHandOverAndBack(t *testing.T) {
	m := castTestModel()
	local := m.player
	m.devicePicker.cursor = 1

	m.handleDeviceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.casting() || m.player == local || m.castDeviceName() != "SHIELD" {
		t.Fatalf("casting %v, device %q; want the SHIELD engine", m.casting(), m.castDeviceName())
	}
	if local.(*playbackFakeEngine).stopCalls == 0 {
		t.Fatal("the local engine kept playing")
	}

	m.devicePicker.visible = true
	m.devicePicker.cursor = 0
	m.handleDeviceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.casting() || m.player != local {
		t.Fatal("choosing the local output did not bring the local engine back")
	}
}

// While casting, provider playback reports stay off: the receiver reports
// the listen itself.
func TestCastSilencesPlaybackReports(t *testing.T) {
	m := castTestModel()
	m.cast.player = cast.NewPlayer(tv, nil)
	if r := m.findPlaybackReporter(playlist.Track{Path: "cast:1"}); r != nil {
		t.Fatal("a provider report runs while casting")
	}
}

// A track that can't cast brings playback back to this device before it
// starts.
func TestUncastableTrackPlaysHere(t *testing.T) {
	m := castTestModel()
	local := m.player
	m.cast = castState{player: cast.NewPlayer(tv, nil), local: local}
	m.player = m.cast.player
	m.uncastFor(playlist.Track{Path: "/music/local.mp3", Title: "Local"})
	if m.casting() || m.player != local {
		t.Fatal("a local track kept the Cast engine")
	}
	if !strings.Contains(m.status.text, "can't play on SHIELD") {
		t.Fatalf("status = %q, want why it plays here", m.status.text)
	}
}

// A track back from a Cast device stays silent until it seeks to where the
// device was, and gets its volume back then, or when the seek never lands.
func TestCastMuteLiftsAfterSeek(t *testing.T) {
	for _, tt := range []struct {
		name     string
		position time.Duration
		resume   string
		after    time.Duration
		lifted   bool
	}{
		{name: "still at 0", position: 0, resume: "cast:1", after: time.Second},
		{name: "seeked", position: 60 * time.Second, after: time.Second, lifted: true},
		{name: "seek never lands", position: 0, resume: "cast:1", after: castMuteTimeout + time.Second, lifted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := castTestModel()
			engine := m.player.(*playbackFakeEngine)
			now := time.Now()
			m.castMute = castMute{active: true, volume: -12, volMin: -50, target: 60 * time.Second, until: now.Add(castMuteTimeout)}
			engine.volume = -90
			engine.position = tt.position
			m.resume.path = tt.resume
			m.tickCastMute(now.Add(tt.after))
			if lifted := engine.volume == -12 && !m.castMute.active; lifted != tt.lifted {
				t.Fatalf("volume %v, mute %v; want lifted %v", engine.volume, m.castMute.active, tt.lifted)
			}
		})
	}
}

// Handing a track to a Cast device ends its listen here, since the device
// reports its own; while it plays there, nothing more goes out from here.
func TestCastEndsLocalListen(t *testing.T) {
	m := castTestModel()
	prov := newTimelineProv()
	m.providers = append(m.providers, provider.Entry{Key: "timeline", Name: "Timeline", Provider: prov})
	track := playlist.Track{Path: "cast:1", Title: "One"}
	m.playlist.Replace([]playlist.Track{track})
	m.playlist.SetIndex(0)
	engine := m.player.(*playbackFakeEngine)
	engine.playing, engine.position = true, 42*time.Second
	m.playingTrack, m.playingTrackActive, m.playingTrackStarted = track, true, true

	m.castTo(tv)
	prov.expect(t, "stopped 42s")
	m.tickProgressReport(time.Now())
	prov.expect(t, "")
}
