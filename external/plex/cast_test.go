package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bjarneo/cliamp/playlist"
)

// The Cast play queue is made as its own client, so it does not end the
// station queue of cliamp's client, and the LOAD media names the server.
func TestCastMediaMakesItsOwnQueue(t *testing.T) {
	var queueClient, queueURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/playQueues" {
			queueClient = r.Header.Get("X-Plex-Client-Identifier")
			queueURI = r.URL.Query().Get("uri")
			w.Write([]byte(`{"MediaContainer":{"playQueueID":77}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok")
	client.cast = &castServer{machineID: "machine", version: "1.0", username: "me"}
	p := newProvider(client)
	if !p.CanCast(playlist.Track{Path: "plex:track:42"}) || p.CanCast(playlist.Track{Path: "/music/a.mp3"}) {
		t.Fatal("CanCast does not tell Plex tracks from others")
	}
	media, err := p.CastMedia(context.Background(), playlist.Track{Path: "plex:track:42"})
	if err != nil {
		t.Fatal(err)
	}
	if queueClient != client.clientID+castClientSuffix {
		t.Fatalf("queue made as client %q, want %q", queueClient, client.clientID+castClientSuffix)
	}
	if queueURI != "server://machine/com.plexapp.plugins.library/library/metadata/42" {
		t.Fatalf("queue uri = %q", queueURI)
	}
	custom := media.Media["customData"].(map[string]any)
	server := custom["server"].(map[string]any)
	if media.AppID != castAppID || media.Media["contentId"] != "/library/metadata/42" ||
		custom["containerKey"] != "/playQueues/77?own=1&window=200" ||
		server["machineIdentifier"] != "machine" || server["accessToken"] != "tok" {
		t.Fatalf("media = %#v", media)
	}
}
