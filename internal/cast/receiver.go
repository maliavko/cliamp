package cast

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// App is a receiver app running on a device.
type App struct {
	AppID       string `json:"appId"`
	DisplayName string `json:"displayName"`
	SessionID   string `json:"sessionId"`
	TransportID string `json:"transportId"`
}

// Volume is the device volume, as a level from 0 to 1.
type Volume struct {
	Level float64 `json:"level"`
	Muted bool    `json:"muted"`
	// StepInterval is the device's volume step; it ignores a smaller change.
	StepInterval float64 `json:"stepInterval"`
}

// ReceiverStatus is what the device runs and its volume.
type ReceiverStatus struct {
	Applications []App  `json:"applications"`
	Volume       Volume `json:"volume"`
}

// App returns the running app with appID.
func (s ReceiverStatus) App(appID string) (App, bool) {
	for _, a := range s.Applications {
		if a.AppID == appID && a.TransportID != "" {
			return a, true
		}
	}
	return App{}, false
}

// ReceiverStatus asks the device what it runs.
func (c *Conn) ReceiverStatus(ctx context.Context) (ReceiverStatus, error) {
	p, err := c.Request(ctx, receiverID, NamespaceReceiver, map[string]any{"type": "GET_STATUS"})
	if err != nil {
		return ReceiverStatus{}, err
	}
	return receiverStatus(p)
}

// Launch starts appID, or finds it already running, and returns it.
func (c *Conn) Launch(ctx context.Context, appID string) (App, error) {
	if st, err := c.ReceiverStatus(ctx); err == nil {
		if app, ok := st.App(appID); ok {
			return app, nil
		}
	}
	p, err := c.Request(ctx, receiverID, NamespaceReceiver, map[string]any{"type": "LAUNCH", "appId": appID})
	if err != nil {
		return App{}, err
	}
	// The reply can come before the app has its transport; ask again until
	// it does.
	for {
		if p["type"] == "LAUNCH_ERROR" {
			return App{}, fmt.Errorf("cast: launch %s: %v", appID, p["reason"])
		}
		if st, err := receiverStatus(p); err == nil {
			if app, ok := st.App(appID); ok {
				return app, nil
			}
		}
		select {
		case <-ctx.Done():
			return App{}, fmt.Errorf("cast: launch %s: %w", appID, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
		if p, err = c.Request(ctx, receiverID, NamespaceReceiver, map[string]any{"type": "GET_STATUS"}); err != nil {
			return App{}, err
		}
	}
}

// SetVolume sets the device volume level, from 0 to 1.
func (c *Conn) SetVolume(ctx context.Context, level float64) error {
	level = min(1, max(0, level))
	_, err := c.Request(ctx, receiverID, NamespaceReceiver, map[string]any{
		"type": "SET_VOLUME", "volume": map[string]any{"level": level},
	})
	return err
}

// SetMuted mutes or unmutes the device without changing its level.
func (c *Conn) SetMuted(muted bool) error {
	return c.Send(receiverID, NamespaceReceiver, map[string]any{
		"type": "SET_VOLUME", "volume": map[string]any{"muted": muted},
	})
}

// StopApp closes the app session, so the device goes back to its home screen.
func (c *Conn) StopApp(ctx context.Context, sessionID string) error {
	_, err := c.Request(ctx, receiverID, NamespaceReceiver, map[string]any{"type": "STOP", "sessionId": sessionID})
	return err
}

func receiverStatus(p map[string]any) (ReceiverStatus, error) {
	var out struct {
		Status ReceiverStatus `json:"status"`
	}
	if err := remarshal(p, &out); err != nil {
		return ReceiverStatus{}, err
	}
	return out.Status, nil
}

// remarshal decodes a generic payload into a typed one.
func remarshal(p map[string]any, v any) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
