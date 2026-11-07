package cast

import (
	"bytes"
	"math"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestMessageRoundTrip(t *testing.T) {
	in := message{source: "sender-0", destination: "receiver-0", namespace: NamespaceReceiver, payload: []byte(`{"type":"GET_STATUS"}`)}
	got, err := readMessage(bytes.NewReader(in.encode()))
	if err != nil {
		t.Fatal(err)
	}
	if got.source != in.source || got.destination != in.destination || got.namespace != in.namespace || string(got.payload) != string(in.payload) {
		t.Fatalf("decoded %+v, want %+v", got, in)
	}
}

func TestReadMessageRejectsOversizedFrame(t *testing.T) {
	if _, err := readMessage(bytes.NewReader([]byte{0x7f, 0xff, 0xff, 0xff})); err == nil {
		t.Fatal("an oversized frame was accepted")
	}
}

// A device answers with PTR, SRV, TXT and A records, which the resolver
// joins into one device.
func TestResolverJoinsRecords(t *testing.T) {
	name := func(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }
	instance := "SHIELD-4a75b6._googlecast._tcp.local."
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	b.EnableCompression()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(b.StartAnswers())
	hdr := func(n string) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: name(n), Class: dnsmessage.ClassINET, TTL: 120}
	}
	must(b.PTRResource(hdr(castService), dnsmessage.PTRResource{PTR: name(instance)}))
	must(b.StartAdditionals())
	must(b.SRVResource(hdr(instance), dnsmessage.SRVResource{Port: 8009, Target: name("4a75b6.local.")}))
	must(b.TXTResource(hdr(instance), dnsmessage.TXTResource{TXT: []string{"id=4a75b6", "md=SHIELD Android TV", "fn=Living Room"}}))
	must(b.AResource(hdr("4a75b6.local."), dnsmessage.AResource{A: [4]byte{192, 168, 1, 39}}))
	packet, err := b.Finish()
	must(err)

	r := newResolver()
	r.add(packet)
	devices := r.devices()
	want := Device{ID: "4a75b6", Name: "Living Room", Model: "SHIELD Android TV", Addr: netip.MustParseAddrPort("192.168.1.39:8009")}
	if len(devices) != 1 || devices[0] != want {
		t.Fatalf("devices = %+v, want %+v", devices, want)
	}
}

// The level scale of the device maps onto the UI's dB range and back.
func TestVolumeMapping(t *testing.T) {
	p := NewPlayer(Device{}, nil)
	for _, level := range []float64{0, 0.28, 0.5, 1} {
		if got := p.dbToLevel(p.levelToDB(level)); got < level-1e-9 || got > level+1e-9 {
			t.Fatalf("level %v came back as %v", level, got)
		}
	}
	if p.dbToLevel(6) != 1 || p.dbToLevel(-90) != 0 {
		t.Fatal("dB outside the range does not clamp")
	}
}

// The Plex receiver reports the end of a track as IDLE without a reason and
// at position 0. Near the end that is the end of the track; mid-track it is
// not, and the position never jumps back to 0.
func TestIdleEndsTrackNearItsEnd(t *testing.T) {
	for _, tt := range []struct {
		name    string
		at      time.Duration
		reason  string
		drained bool
	}{
		{name: "end without reason", at: 296 * time.Second, drained: true},
		{name: "finished", at: 100 * time.Second, reason: IdleFinished, drained: true},
		{name: "error", at: 100 * time.Second, reason: "ERROR", drained: true},
		{name: "mid-track without reason", at: 100 * time.Second},
		{name: "interrupted", at: 296 * time.Second, reason: "INTERRUPTED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPlayer(Device{}, nil)
			p.loaded, p.session, p.state = true, 1, StatePlaying
			p.duration = 297 * time.Second
			p.base, p.baseAt = tt.at, time.Now()
			p.applyStatus([]MediaStatus{{SessionID: 1, PlayerState: StateIdle, IdleReason: tt.reason}})
			if p.Drained() != tt.drained {
				t.Fatalf("drained = %v, want %v", p.Drained(), tt.drained)
			}
			if pos := p.Position(); pos < tt.at-time.Second {
				t.Fatalf("position jumped to %s from %s", pos, tt.at)
			}
		})
	}
}

// The Plex receiver closes itself at the end of a track, at times before it
// reports the track idle. At the end, or after the idle report, that ends the
// track; mid-track the device ended the session.
func TestAppClosingAtTheEndEndsTrack(t *testing.T) {
	for _, tt := range []struct {
		name    string
		at      time.Duration
		idle    bool
		drained bool
	}{
		{name: "near the end", at: 296 * time.Second, drained: true},
		{name: "after the idle report", at: 296 * time.Second, idle: true, drained: true},
		{name: "mid-track", at: 100 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPlayer(Device{}, nil)
			p.loaded, p.session, p.state = true, 1, StatePlaying
			p.app = App{AppID: "9AC194DC", SessionID: "s1", TransportID: "t1"}
			p.duration = 297 * time.Second
			p.base, p.baseAt = tt.at, time.Now()
			if tt.idle {
				p.applyStatus([]MediaStatus{{SessionID: 1, PlayerState: StateIdle}})
			}
			p.applyReceiver(map[string]any{"type": "RECEIVER_STATUS", "status": map[string]any{
				"applications": []any{map[string]any{"appId": "E8C28D3C", "sessionId": "home", "transportId": "t0"}},
			}})
			if p.Drained() != tt.drained || p.IsPlaying() != tt.drained || p.Left() == tt.drained {
				t.Fatalf("drained %v, playing %v, left %v; want drained and playing %v", p.Drained(), p.IsPlaying(), p.Left(), tt.drained)
			}
		})
	}
}

// A volume change smaller than the device step still moves one step, on the
// device's grid.
func TestVolumeMovesByDeviceStep(t *testing.T) {
	p := NewPlayer(Device{}, nil)
	p.SetVolumeMin(-50)
	p.volStep = 1.0 / 15
	p.volume = p.levelToDB(8.0 / 15)
	p.SetVolume(p.Volume() + 1)
	if got := p.dbToLevel(p.Volume()); math.Abs(got-9.0/15) > 1e-9 {
		t.Fatalf("level after +1 dB = %v, want one step up, %v", got, 9.0/15)
	}
	p.SetVolume(p.Volume() - 1)
	if got := p.dbToLevel(p.Volume()); math.Abs(got-8.0/15) > 1e-9 {
		t.Fatalf("level after -1 dB = %v, want one step down, %v", got, 8.0/15)
	}
}
