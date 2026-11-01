package cast

import (
	"context"
	"fmt"
)

// Player states of a media session.
const (
	StatePlaying   = "PLAYING"
	StatePaused    = "PAUSED"
	StateBuffering = "BUFFERING"
	StateIdle      = "IDLE"
)

// IdleFinished is the idle reason of a session whose media played to its end.
const IdleFinished = "FINISHED"

// MediaStatus is the state of one media session of an app.
type MediaStatus struct {
	SessionID   int     `json:"mediaSessionId"`
	PlayerState string  `json:"playerState"`
	IdleReason  string  `json:"idleReason"`
	CurrentTime float64 `json:"currentTime"`
	Media       *struct {
		ContentID string  `json:"contentId"`
		Duration  float64 `json:"duration"`
	} `json:"media"`
}

// MediaStatuses reads the statuses of a MEDIA_STATUS payload.
func MediaStatuses(p map[string]any) []MediaStatus {
	var out struct {
		Status []MediaStatus `json:"status"`
	}
	if remarshal(p, &out) != nil {
		return nil
	}
	return out.Status
}

// Load asks the app at transport to play media. media is the "media" object
// of a LOAD request, which the app defines; customData carries what a
// particular receiver needs.
func (c *Conn) Load(ctx context.Context, transport string, media map[string]any) (MediaStatus, error) {
	p, err := c.Request(ctx, transport, NamespaceMedia, map[string]any{"type": "LOAD", "media": media})
	if err != nil {
		return MediaStatus{}, err
	}
	if t, _ := p["type"].(string); t != "MEDIA_STATUS" {
		return MediaStatus{}, fmt.Errorf("cast: load: %s", t)
	}
	st := MediaStatuses(p)
	if len(st) == 0 {
		return MediaStatus{}, fmt.Errorf("cast: load: no media session")
	}
	return st[0], nil
}

// MediaStatus asks the app at transport for its media session.
func (c *Conn) MediaStatus(ctx context.Context, transport string) (MediaStatus, bool, error) {
	p, err := c.Request(ctx, transport, NamespaceMedia, map[string]any{"type": "GET_STATUS"})
	if err != nil {
		return MediaStatus{}, false, err
	}
	st := MediaStatuses(p)
	if len(st) == 0 {
		return MediaStatus{}, false, nil
	}
	return st[0], true, nil
}

// Play resumes the media session.
func (c *Conn) Play(ctx context.Context, transport string, session int) error {
	return c.mediaCommand(ctx, transport, session, map[string]any{"type": "PLAY"})
}

// Pause pauses the media session.
func (c *Conn) Pause(ctx context.Context, transport string, session int) error {
	return c.mediaCommand(ctx, transport, session, map[string]any{"type": "PAUSE"})
}

// Stop ends the media session.
func (c *Conn) Stop(ctx context.Context, transport string, session int) error {
	return c.mediaCommand(ctx, transport, session, map[string]any{"type": "STOP"})
}

// Seek moves the media session to seconds.
func (c *Conn) Seek(ctx context.Context, transport string, session int, seconds float64) error {
	return c.mediaCommand(ctx, transport, session, map[string]any{"type": "SEEK", "currentTime": seconds})
}

// mediaCommand sends a command to the media session without waiting for a
// reply. Receivers report the new state in a MEDIA_STATUS to all senders,
// and some, such as Plex's, give that report no requestId.
func (c *Conn) mediaCommand(_ context.Context, transport string, session int, payload map[string]any) error {
	payload["mediaSessionId"] = session
	return c.Send(transport, NamespaceMedia, payload)
}
