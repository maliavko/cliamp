package cast

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakeReceiver is a Cast device that runs one app. The first app instance
// closes when it gets a LOAD, as the Plex receiver does when its one-track
// queue ends just as the next track loads; the next instance plays it.
type fakeReceiver struct {
	t    *testing.T
	addr netip.AddrPort

	mu        sync.Mutex
	transport string // the transport of the running app, "" when none
	instances int
	loads     []string // the transports that got a LOAD
}

const fakeAppID = "9AC194DC"

func newFakeReceiver(t *testing.T) *fakeReceiver {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeReceiver{t: t, addr: netip.MustParseAddrPort(ln.Addr().String())}
	f.launch()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

// launch starts a new instance of the app. The caller holds f.mu, or no
// connection exists yet.
func (f *fakeReceiver) launch() {
	f.instances++
	f.transport = "web-" + string(rune('0'+f.instances))
}

func (f *fakeReceiver) serve(c net.Conn) {
	defer c.Close()
	var writeMu sync.Mutex
	write := func(source, destination, namespace string, payload map[string]any) {
		raw, _ := json.Marshal(payload)
		writeMu.Lock()
		defer writeMu.Unlock()
		_, _ = c.Write(message{source: source, destination: destination, namespace: namespace, payload: raw}.encode())
	}
	for {
		m, err := readMessage(c)
		if err != nil {
			return
		}
		var p map[string]any
		if json.Unmarshal(m.payload, &p) != nil {
			continue
		}
		id := p["requestId"]
		f.mu.Lock()
		switch {
		case m.namespace == NamespaceReceiver:
			switch {
			case p["type"] == "LAUNCH" && f.transport == "":
				f.launch()
			case p["type"] == "STOP":
				f.transport = ""
			}
			var apps []any
			if f.transport != "" {
				apps = append(apps, map[string]any{"appId": fakeAppID, "sessionId": "s-" + f.transport, "transportId": f.transport})
			}
			write(receiverID, m.source, NamespaceReceiver, map[string]any{
				"type": "RECEIVER_STATUS", "requestId": id,
				"status": map[string]any{"applications": apps, "volume": map[string]any{"level": 0.5}},
			})
		case m.namespace == NamespaceMedia && p["type"] == "LOAD":
			f.loads = append(f.loads, m.destination)
			if f.instances == 1 {
				// The first instance closes without an answer.
				f.transport = ""
				write(m.destination, m.source, NamespaceConnection, map[string]any{"type": "CLOSE"})
				break
			}
			write(m.destination, m.source, NamespaceMedia, map[string]any{
				"type": "MEDIA_STATUS", "requestId": id,
				"status": []any{map[string]any{"mediaSessionId": 1, "playerState": StateBuffering}},
			})
		}
		f.mu.Unlock()
	}
}

// A track that loads as the app closes launches the app again and plays on
// the new instance, instead of waiting out the whole load timeout.
func TestLoadRelaunchesAppThatClosed(t *testing.T) {
	f := newFakeReceiver(t)
	resolve := func(context.Context, string) (string, map[string]any, error) {
		return fakeAppID, map[string]any{"contentId": "/library/metadata/1"}, nil
	}
	p := NewPlayer(Device{Name: "fake", Addr: f.addr}, resolve)
	t.Cleanup(p.Close)

	start := time.Now()
	if err := p.PlayAt("plex:track:1", time.Minute, 0); err != nil {
		t.Fatalf("PlayAt: %v", err)
	}
	// Without the CLOSE ending the load, it waits out a 10 s timeout.
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("PlayAt took %s, want a prompt load on the new app", took)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []string{"web-1", "web-2"}; len(f.loads) != 2 || f.loads[0] != want[0] || f.loads[1] != want[1] {
		t.Fatalf("loads went to %v, want %v", f.loads, want)
	}
	if !p.IsPlaying() {
		t.Fatal("the track is not on the device")
	}
}
