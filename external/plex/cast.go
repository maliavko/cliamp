package plex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bjarneo/cliamp/playlist"
	"github.com/bjarneo/cliamp/provider"
)

// castAppID is the Plex receiver for Google Cast devices, the one Plex and
// Plexamp start when they cast. It plays a server play queue in the
// original quality and shows the artwork.
const castAppID = "9AC194DC"

// castClientSuffix marks the client that makes the play queues of a Cast
// device. The server keeps one play queue per client.
const castClientSuffix = "-cast"

// castServer is what the Plex receiver needs to reach the server.
type castServer struct {
	machineID string
	version   string
	username  string
	plexPass  bool
}

// CanCast reports whether track is a Plex track, which the Plex receiver can
// play. It does no I/O.
func (p *Provider) CanCast(track playlist.Track) bool {
	return strings.HasPrefix(track.Path, TrackURIPrefix)
}

// CastMedia starts a server play queue with track and returns the LOAD media
// that plays it on the Plex receiver.
func (p *Provider) CastMedia(ctx context.Context, track playlist.Track) (provider.CastMedia, error) {
	ratingKey, ok := strings.CutPrefix(track.Path, TrackURIPrefix)
	if !ok || ratingKey == "" {
		return provider.CastMedia{}, fmt.Errorf("plex: cast: %q is not a Plex track", track.Path)
	}
	return p.client.castMedia(ctx, ratingKey)
}

func (c *Client) castMedia(ctx context.Context, ratingKey string) (provider.CastMedia, error) {
	server, err := c.castServer(ctx)
	if err != nil {
		return provider.CastMedia{}, err
	}

	var created struct {
		MediaContainer struct {
			PlayQueueID int `json:"playQueueID"`
		} `json:"MediaContainer"`
	}
	params := url.Values{
		"type": {"audio"},
		"uri":  {"server://" + server.machineID + "/com.plexapp.plugins.library/library/metadata/" + ratingKey},
	}
	// As its own client, so this queue does not end the station
	// queue that cliamp continues.
	if err := c.requestAs(c.clientID+castClientSuffix, http.MethodPost, "/playQueues", params, &created); err != nil {
		return provider.CastMedia{}, fmt.Errorf("plex: cast: play queue: %w", err)
	}
	if created.MediaContainer.PlayQueueID == 0 {
		return provider.CastMedia{}, fmt.Errorf("plex: cast: no play queue")
	}

	// The requests above may have moved to another server route, so the
	// address the receiver gets is read after them.
	baseURL, token, err := c.endpoint(ctx)
	if err != nil {
		return provider.CastMedia{}, fmt.Errorf("plex: cast: connect: %w", err)
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return provider.CastMedia{}, fmt.Errorf("plex: cast: server URL: %w", err)
	}
	port := 32400
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	} else if u.Scheme == "https" {
		port = 443
	}

	// The receiver fetches the queue and the stream itself, so customData
	// names the server, as Plex's own senders do. Without the version, the
	// user and the transcoder flags it loads the track but never plays it.
	return provider.CastMedia{AppID: castAppID, Media: map[string]any{
		"contentId":   "/library/metadata/" + ratingKey,
		"streamType":  "BUFFERED",
		"contentType": "music",
		"customData": map[string]any{
			"offset":       0,
			"directPlay":   true,
			"directStream": true,
			"audioBoost":   100,
			"server": map[string]any{
				"machineIdentifier":        server.machineID,
				"transcoderVideo":          true,
				"transcoderVideoRemuxOnly": false,
				"transcoderAudio":          true,
				"version":                  server.version,
				"myPlexSubscription":       server.plexPass,
				"isVerifiedHostname":       true,
				"protocol":                 u.Scheme,
				"address":                  u.Hostname(),
				"port":                     port,
				"accessToken":              token,
				"user":                     map[string]any{"username": server.username},
			},
			"containerKey": "/playQueues/" + strconv.Itoa(created.MediaContainer.PlayQueueID) + "?own=1&window=200",
		},
		"autoplay":       true,
		"currentTime":    0,
		"activeTrackIds": nil,
	}}, nil
}

// castServer reads the server identity and version and the account's user
// name, once per client.
func (c *Client) castServer(ctx context.Context) (castServer, error) {
	c.mu.Lock()
	cached := c.cast
	c.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	var identity struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
			Version           string `json:"version"`
		} `json:"MediaContainer"`
	}
	if err := c.get("/identity", nil, &identity); err != nil {
		return castServer{}, fmt.Errorf("plex: cast: server identity: %w", err)
	}
	s := castServer{machineID: identity.MediaContainer.MachineIdentifier, version: identity.MediaContainer.Version}
	if s.machineID == "" {
		return castServer{}, fmt.Errorf("plex: cast: server identity: no machine identifier")
	}
	// The user name only labels the session on the receiver; casting goes
	// on without it.
	if user, err := c.accountUser(ctx); err == nil {
		s.username, s.plexPass = user.Username, user.Subscription.Active
	}
	c.mu.Lock()
	c.cast = &s
	c.mu.Unlock()
	return s, nil
}

type accountUser struct {
	Username     string `json:"username"`
	Subscription struct {
		Active bool `json:"active"`
	} `json:"subscription"`
}

// accountUser reads the signed-in account from plex.tv. A browser sign-in
// keeps the account token apart from the server token; a manual token is
// both.
func (c *Client) accountUser(ctx context.Context) (accountUser, error) {
	c.mu.Lock()
	api, token, clientID := NewAccountAPI(), c.token, c.clientID
	if c.account != nil {
		api, token, clientID = c.account.api, c.account.token, c.account.clientID
	}
	c.mu.Unlock()
	var user accountUser
	err := api.doJSON(ctx, http.MethodGet, "/api/v2/user", clientID, token, &user)
	return user, err
}
