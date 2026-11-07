package cast

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// Resolver turns a track path into the receiver app that plays it and the
// media of the LOAD request. It runs off the UI goroutine and may do I/O.
type Resolver func(ctx context.Context, path string) (appID string, media map[string]any, err error)

// commandTimeout bounds one request to the device.
const commandTimeout = 10 * time.Second

// startSeekTimeout unmutes a track that started muted for its start seek
// when the device never reports the new position.
const startSeekTimeout = 10 * time.Second

// reportSlack is how far the counted position may stray from a report of the
// device before it follows the report.
const reportSlack = 1500 * time.Millisecond

// statusInterval is how often the player asks for the media status while a
// track plays, to correct the position it counts between reports.
const statusInterval = 5 * time.Second

var errNoPreload = errors.New("cast: no gapless preload")

// Player plays tracks on a Cast device. It has the methods of player.Engine,
// so the UI drives it like the local audio engine. The device plays one
// track per LOAD; the UI moves to the next track when one ends, as it does
// without gapless playback. Commands that the UI sends from its own
// goroutine, such as pause and seek, go out in the background, and the
// player updates its state at once.
type Player struct {
	device  Device
	resolve Resolver

	mu       sync.Mutex
	conn     *Conn
	app      App
	session  int
	loaded   bool // a track is on the device and was not stopped
	paused   bool
	drained  bool
	state    string
	base     time.Duration // position at baseAt
	baseAt   time.Time
	duration time.Duration
	err      error
	volume   float64 // dB, as the UI shows it
	volMin   float64
	volStep  float64 // the device's volume step, as a level
	left     bool    // the device ended the session
	eq       [10]float64
	gen      uint64
	preload  uint64
	stop     chan struct{}

	// startAt is where a track that starts mid-way goes once it plays. A
	// receiver such as Plex's ignores a start offset and a seek while it
	// buffers, so the track starts muted from 0 and seeks when its position
	// first moves.
	startAt   time.Duration
	seekSent  bool
	mutedTill time.Time
}

// NewPlayer returns a player for device that resolves tracks with resolve.
// It connects on the first track.
func NewPlayer(device Device, resolve Resolver) *Player {
	return &Player{device: device, resolve: resolve, volMin: -30, stop: make(chan struct{})}
}

// Device returns the device the player plays on.
func (p *Player) Device() Device { return p.device }

// PlayAt plays path from offset on the device.
func (p *Player) PlayAt(path string, knownDuration, offset time.Duration) error {
	return p.PlayAtForGeneration(path, knownDuration, offset, p.currentGen())
}

// PlayAtForGeneration plays path from offset. It blocks while the device
// starts the receiver app and loads the track, so the UI calls it from a
// command.
func (p *Player) PlayAtForGeneration(path string, knownDuration, offset time.Duration, gen uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*commandTimeout)
	defer cancel()
	appID, media, err := p.resolve(ctx, path)
	if err != nil {
		return p.setErr(err)
	}
	conn, err := p.connection(ctx)
	if err != nil {
		return p.setErr(err)
	}
	app, err := conn.Launch(ctx, appID)
	if err != nil {
		return p.setErr(err)
	}
	if offset > 0 {
		_ = conn.SetMuted(true)
	}
	st, err := conn.Load(ctx, app.TransportID, media)
	if err != nil {
		if offset > 0 {
			_ = conn.SetMuted(false)
		}
		return p.setErr(err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.app, p.session = app, st.SessionID
	p.loaded, p.paused, p.drained, p.left, p.err = true, false, false, false, nil
	p.state = StateBuffering
	p.base, p.baseAt = offset, time.Now()
	p.duration = knownDuration
	if st.Media != nil && st.Media.Duration > 0 {
		p.duration = seconds(st.Media.Duration)
	}
	p.startAt, p.seekSent = offset, false
	if offset > 0 {
		p.mutedTill = time.Now().Add(startSeekTimeout)
	}
	_ = gen
	return nil
}

// connection returns the open connection, dialing the device and reading its
// volume when there is none.
func (p *Player) connection(ctx context.Context) (*Conn, error) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn != nil && conn.Err() == nil {
		return conn, nil
	}
	conn, err := Dial(ctx, p.device.Addr.String())
	if err != nil {
		return nil, err
	}
	st, err := conn.ReceiverStatus(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	p.mu.Lock()
	p.conn = conn
	p.volume = p.levelToDB(st.Volume.Level)
	p.volStep = st.Volume.StepInterval
	p.mu.Unlock()
	go p.watch(conn)
	return conn, nil
}

// watch follows the media status of the device: the reports it sends on
// changes, and a request every statusInterval while a track plays.
func (p *Player) watch(conn *Conn) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-conn.Events():
			if !ok {
				return
			}
			if ev.Namespace == NamespaceMedia && ev.Payload["type"] == "MEDIA_STATUS" {
				p.applyStatus(MediaStatuses(ev.Payload))
			}
			if ev.Namespace == NamespaceReceiver && ev.Payload["type"] == "RECEIVER_STATUS" {
				p.applyReceiver(ev.Payload)
			}
		case <-ticker.C:
			p.mu.Lock()
			transport, active := p.app.TransportID, p.loaded && !p.drained
			p.mu.Unlock()
			if !active {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
			st, ok, err := conn.MediaStatus(ctx, transport)
			cancel()
			if err == nil && ok {
				p.applyStatus([]MediaStatus{st})
			}
		case <-conn.Done():
			return
		case <-p.stop:
			return
		}
	}
}

// applyStatus takes the state of the loaded track from a media status.
func (p *Player) applyStatus(statuses []MediaStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range statuses {
		if st.SessionID != p.session || !p.loaded {
			continue
		}
		if p.startAt > 0 && p.startSeek(st) {
			continue
		}
		if st.PlayerState == StateIdle {
			p.applyIdle(st)
			continue
		}
		changed := p.state != st.PlayerState
		p.state = st.PlayerState
		switch st.PlayerState {
		case StatePaused:
			p.paused = true
		case StatePlaying:
			p.paused = false
		}
		// Reports count whole seconds, so the position follows one only on a
		// state change or when it drifts: otherwise it would step back by up
		// to a second at each report.
		reported := seconds(st.CurrentTime)
		if drift := reported - p.positionLocked(); changed || drift > reportSlack || drift < -reportSlack {
			p.base, p.baseAt = reported, time.Now()
		}
		if st.Media != nil && st.Media.Duration > 0 {
			p.duration = seconds(st.Media.Duration)
		}
	}
}

// endSlack is how close to its end a track that goes idle must be to count
// as played to the end.
const endSlack = 3 * time.Second

// applyIdle ends the track. The Plex receiver reports the end of a track as
// IDLE without a reason and at position 0, so a track that goes idle within
// endSlack of its end counts as finished too. An error ends it as well, so
// the UI moves on. The position stays where it was. The caller holds p.mu.
func (p *Player) applyIdle(st MediaStatus) {
	switch {
	case st.IdleReason == IdleFinished, st.IdleReason == "" && p.nearEndLocked():
		p.endTrackLocked()
	case st.IdleReason == "ERROR":
		p.endTrackLocked()
		p.err = errors.New("cast: the device could not play the track")
	}
}

// nearEndLocked reports whether the track is within endSlack of its end. The
// caller holds p.mu.
func (p *Player) nearEndLocked() bool {
	return p.duration > 0 && p.positionLocked() >= p.duration-endSlack
}

// endTrackLocked marks the track as played to its end, keeping its position.
// The caller holds p.mu.
func (p *Player) endTrackLocked() {
	p.base, p.baseAt = p.positionLocked(), time.Now()
	p.state, p.drained = StateIdle, true
}

// startSeek moves a track that started muted to startAt once its position
// first moves, and unmutes it once the position moves past startAt. It
// reports whether the status is still from before the seek, which the
// position must not follow. The caller holds p.mu.
func (p *Player) startSeek(st MediaStatus) bool {
	conn, transport, session, target := p.conn, p.app.TransportID, p.session, p.startAt
	switch {
	case time.Now().After(p.mutedTill):
		p.startAt = 0
	case !p.seekSent && st.PlayerState == StatePlaying && st.CurrentTime > 0:
		p.seekSent = true
		_ = conn.Seek(context.Background(), transport, session, target.Seconds())
		return true
	case p.seekSent && seconds(st.CurrentTime) >= target+time.Second:
		// Unmute only once the position moves past the target: the first
		// report after the seek comes while the device still plays out
		// audio from before it.
		p.startAt = 0
	default:
		return true
	}
	_ = conn.SetMuted(false)
	return false
}

// applyReceiver notices that the receiver app went away, as when another
// sender starts its own app, and reads the device volume.
func (p *Player) applyReceiver(payload map[string]any) {
	st, err := receiverStatus(payload)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volume = p.levelToDB(st.Volume.Level)
	if st.Volume.StepInterval > 0 {
		p.volStep = st.Volume.StepInterval
	}
	if !p.loaded || p.app.AppID == "" {
		return
	}
	if app, ok := st.App(p.app.AppID); !ok || app.SessionID != p.app.SessionID {
		// The Plex receiver closes itself when its one-track queue ends,
		// sometimes before it reports the track idle. At the end of the track
		// that is the end of the track, and the next one launches it again.
		if p.drained || p.nearEndLocked() {
			p.endTrackLocked()
			return
		}
		p.loaded, p.left = false, true
	}
}

// background runs a command on the device off the caller's goroutine and
// keeps its error for StreamErr.
func (p *Player) background(cmd func(ctx context.Context, conn *Conn, transport string, session int) error) {
	p.mu.Lock()
	conn, transport, session := p.conn, p.app.TransportID, p.session
	p.mu.Unlock()
	if conn == nil || transport == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if err := cmd(ctx, conn, transport, session); err != nil {
			_ = p.setErr(err)
		}
	}()
}

// TogglePause pauses or resumes the track.
func (p *Player) TogglePause() {
	p.mu.Lock()
	if !p.loaded {
		p.mu.Unlock()
		return
	}
	p.base, p.baseAt = p.positionLocked(), time.Now()
	p.paused = !p.paused
	paused := p.paused
	p.mu.Unlock()
	p.background(func(ctx context.Context, c *Conn, t string, s int) error {
		if paused {
			return c.Pause(ctx, t, s)
		}
		return c.Play(ctx, t, s)
	})
}

// Seek moves the track by d.
func (p *Player) Seek(d time.Duration) error {
	p.mu.Lock()
	if !p.loaded {
		p.mu.Unlock()
		return errors.New("cast: nothing playing")
	}
	target := max(0, p.positionLocked()+d)
	if p.duration > 0 {
		target = min(target, p.duration)
	}
	p.base, p.baseAt = target, time.Now()
	p.mu.Unlock()
	p.background(func(ctx context.Context, c *Conn, t string, s int) error {
		return c.Seek(ctx, t, s, target.Seconds())
	})
	return nil
}

// Stop ends the track on the device.
func (p *Player) Stop() {
	p.mu.Lock()
	wasLoaded := p.loaded && !p.drained
	p.loaded, p.paused, p.drained = false, false, false
	p.mu.Unlock()
	if wasLoaded {
		p.background(func(ctx context.Context, c *Conn, t string, s int) error {
			return c.Stop(ctx, t, s)
		})
	}
}

// Close stops the receiver app, so the device goes back to its home screen,
// and closes the connection.
func (p *Player) Close() {
	p.mu.Lock()
	conn, sessionID := p.conn, p.app.SessionID
	p.conn, p.loaded = nil, false
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	p.mu.Unlock()
	if conn == nil {
		return
	}
	if sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		_ = conn.StopApp(ctx, sessionID)
		cancel()
	}
	conn.Close()
}

// Left reports that the device ended the session: someone closed the app on
// the device, or another sender started its own. Unlike an error, it is not
// a reason to play the track again.
func (p *Player) Left() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.left
}

// IsPlaying reports whether a track is on the device, playing or paused.
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loaded
}

// IsPaused reports whether the track is paused.
func (p *Player) IsPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// Drained reports whether the track played to its end.
func (p *Player) Drained() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.drained
}

// Position counts the position from the last report of the device.
func (p *Player) Position() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.positionLocked()
}

func (p *Player) positionLocked() time.Duration {
	pos := p.base
	if p.loaded && !p.paused && !p.drained && p.state == StatePlaying {
		pos += time.Since(p.baseAt)
	}
	if p.duration > 0 {
		pos = min(pos, p.duration)
	}
	return pos
}

// Duration returns the length of the track.
func (p *Player) Duration() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.duration
}

// PositionAndDuration returns both at once.
func (p *Player) PositionAndDuration() (time.Duration, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.positionLocked(), p.duration
}

// LastPlayedDuration returns how far the track played.
func (p *Player) LastPlayedDuration() time.Duration { return p.Position() }

// SetVolume sets the device volume. The UI's range from VolumeMin to 0 dB
// maps onto the device level from 0 to 1.
func (p *Player) SetVolume(db float64) {
	if math.IsNaN(db) {
		return
	}
	p.mu.Lock()
	level := p.stepLevel(p.dbToLevel(p.volume), p.dbToLevel(max(min(db, 0), p.volMin)))
	p.volume = p.levelToDB(level)
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if err := conn.SetVolume(ctx, level); err != nil {
			_ = p.setErr(err)
		}
	}()
}

// Volume returns the device volume in dB.
func (p *Player) Volume() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.volume
}

// SetVolumeMin sets the bottom of the volume range.
func (p *Player) SetVolumeMin(db float64) {
	if math.IsNaN(db) {
		return
	}
	p.mu.Lock()
	p.volMin = max(min(db, -1), -90)
	p.mu.Unlock()
}

// VolumeMin returns the bottom of the volume range.
func (p *Player) VolumeMin() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.volMin
}

// stepLevel moves the level from current toward target on the device's step
// grid, by at least one step, since the device ignores a smaller change.
func (p *Player) stepLevel(current, target float64) float64 {
	step := p.volStep
	if step <= 0 || target == current {
		return target
	}
	n := math.Round(target / step)
	if math.Abs(target-current) < step {
		n = math.Round(current/step) + math.Copysign(1, target-current)
	}
	return min(1, max(0, n*step))
}

func (p *Player) dbToLevel(db float64) float64 {
	return min(1, max(0, (db-p.volMin)/-p.volMin))
}

func (p *Player) levelToDB(level float64) float64 {
	return p.volMin + min(1, max(0, level))*-p.volMin
}

// StreamErr returns the last error of a command to the device.
func (p *Player) StreamErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Player) setErr(err error) error {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	return err
}

func (p *Player) currentGen() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// SetPlaybackGeneration records the UI's playback generation.
func (p *Player) SetPlaybackGeneration(generation uint64) {
	p.mu.Lock()
	p.gen = generation
	p.mu.Unlock()
}

// BeginPreload starts a preload generation. The device has no gapless
// queue, so every preload fails and the UI starts the next track itself.
func (p *Player) BeginPreload() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preload++
	return p.preload
}

// PreloadForGeneration refuses: the device plays one track per LOAD.
func (p *Player) PreloadForGeneration(string, time.Duration, uint64) error { return errNoPreload }

// The rest of the engine is the local audio chain, which the device does
// not have: no preload, no yt-dlp seek, no live streams, no speed, mono, EQ
// or visualizer samples.

func (p *Player) ClearPreload()                      {}
func (p *Player) SeekYTDL(time.Duration) error       { return errors.New("cast: no yt-dlp seek") }
func (p *Player) CancelSeekYTDL()                    {}
func (p *Player) HasPreload() bool                   { return false }
func (p *Player) Seekable() bool                     { return true }
func (p *Player) IsYTDLSeek() bool                   { return false }
func (p *Player) IsLiveStream() bool                 { return false }
func (p *Player) GaplessAdvanced() bool              { return false }
func (p *Player) SetSpeed(float64)                   {}
func (p *Player) Speed() float64                     { return 1 }
func (p *Player) ToggleMono()                        {}
func (p *Player) Mono() bool                         { return false }
func (p *Player) StreamTitle() string                { return "" }
func (p *Player) StreamBytes() (int64, int64)        { return 0, 0 }
func (p *Player) SamplesInto([]float64) int          { return 0 }
func (p *Player) WaveformSamplesInto([]float64) int  { return 0 }
func (p *Player) StereoSamplesInto([][2]float64) int { return 0 }
func (p *Player) SampleRate() int                    { return 44100 }

// SetEQBand keeps the band so EQBands reports it back; the device has no EQ.
func (p *Player) SetEQBand(band int, dB float64) {
	if band < 0 || band >= len(p.eq) {
		return
	}
	p.mu.Lock()
	p.eq[band] = dB
	p.mu.Unlock()
}

// EQBands returns the bands the UI set.
func (p *Player) EQBands() [10]float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eq
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
