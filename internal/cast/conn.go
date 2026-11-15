package cast

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// heartbeatInterval keeps the device from closing an idle connection, which
// it does after a few missed pings.
const heartbeatInterval = 5 * time.Second

// ErrClosed is returned by calls on a closed connection.
var ErrClosed = errors.New("cast: connection closed")

// ErrAppClosed is returned by a request whose app closed before it answered.
var ErrAppClosed = errors.New("cast: the app closed")

// pendingRequest waits for the reply to one request.
type pendingRequest struct {
	destination string
	reply       chan map[string]any
	closed      chan struct{} // closed when destination closes the connection
}

// Conn is a connection to one Cast device. Requests wait for the reply with
// their requestId; messages that answer no request, such as a status the
// device sends on its own, go to Events.
type Conn struct {
	tc     *tls.Conn
	source string

	writeMu sync.Mutex

	mu        sync.Mutex
	nextID    int
	pending   map[int]pendingRequest
	connected map[string]bool
	err       error

	events chan Event
	done   chan struct{}
}

// Event is a message from the device that answers no request.
type Event struct {
	Source    string
	Namespace string
	Payload   map[string]any
}

// Dial opens a connection to the device at addr (host:port) and opens the
// virtual connection to the device's receiver.
func Dial(ctx context.Context, addr string) (*Conn, error) {
	// Cast devices present a self-signed certificate; the protocol has its
	// own device authentication, which a sender is free to skip.
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	nc, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("cast: dial %s: %w", addr, err)
	}
	c := &Conn{
		tc:        nc.(*tls.Conn),
		source:    "sender-cliamp",
		pending:   make(map[int]pendingRequest),
		connected: make(map[string]bool),
		events:    make(chan Event, 16),
		done:      make(chan struct{}),
	}
	go c.readLoop()
	go c.heartbeat()
	if err := c.connect(receiverID); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Events delivers messages that answer no request. It is closed when the
// connection ends.
func (c *Conn) Events() <-chan Event { return c.events }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended, or nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection.
func (c *Conn) Close() error {
	c.fail(ErrClosed)
	return c.tc.Close()
}

// Request sends payload to destination on namespace and waits for the reply
// that carries the same requestId.
func (c *Conn) Request(ctx context.Context, destination, namespace string, payload map[string]any) (map[string]any, error) {
	if err := c.connect(destination); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.nextID++
	id := c.nextID
	req := pendingRequest{destination: destination, reply: make(chan map[string]any, 1), closed: make(chan struct{})}
	c.pending[id] = req
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	payload["requestId"] = id
	if err := c.send(destination, namespace, payload); err != nil {
		return nil, err
	}
	select {
	case p := <-req.reply:
		return p, nil
	case <-req.closed:
		return nil, fmt.Errorf("cast: %s: %w", destination, ErrAppClosed)
	case <-c.done:
		return nil, c.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Send sends payload to destination on namespace without waiting for a
// reply, for messages that get none.
func (c *Conn) Send(destination, namespace string, payload map[string]any) error {
	if err := c.connect(destination); err != nil {
		return err
	}
	c.mu.Lock()
	c.nextID++
	payload["requestId"] = c.nextID
	c.mu.Unlock()
	return c.send(destination, namespace, payload)
}

// connect opens the virtual connection to destination once.
func (c *Conn) connect(destination string) error {
	c.mu.Lock()
	done := c.connected[destination]
	c.connected[destination] = true
	c.mu.Unlock()
	if done {
		return nil
	}
	return c.send(destination, NamespaceConnection, map[string]any{"type": "CONNECT"})
}

func (c *Conn) send(destination, namespace string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cast: encode %s: %w", namespace, err)
	}
	frame := message{source: c.source, destination: destination, namespace: namespace, payload: raw}.encode()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	_ = c.tc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.tc.Write(frame); err != nil {
		c.fail(fmt.Errorf("cast: write: %w", err))
		return c.Err()
	}
	return nil
}

func (c *Conn) readLoop() {
	defer close(c.events)
	for {
		m, err := readMessage(c.tc)
		if err != nil {
			c.fail(fmt.Errorf("cast: read: %w", err))
			return
		}
		var p map[string]any
		if json.Unmarshal(m.payload, &p) != nil {
			continue
		}
		switch {
		case m.namespace == NamespaceHeartbeat:
			if p["type"] == "PING" {
				_ = c.send(m.source, NamespaceHeartbeat, map[string]any{"type": "PONG"})
			}
			continue
		case m.namespace == NamespaceConnection && p["type"] == "CLOSE":
			// The app closed its side, as when another sender takes over.
			// Its requests get no answer now.
			c.mu.Lock()
			delete(c.connected, m.source)
			for id, req := range c.pending {
				if req.destination == m.source {
					close(req.closed)
					delete(c.pending, id)
				}
			}
			c.mu.Unlock()
		}
		if id, ok := p["requestId"].(float64); ok && id > 0 {
			c.mu.Lock()
			req, ok := c.pending[int(id)]
			c.mu.Unlock()
			if ok {
				req.reply <- p
				continue
			}
		}
		select {
		case c.events <- Event{Source: m.source, Namespace: m.namespace, Payload: p}:
		case <-c.done:
			return
		}
	}
}

func (c *Conn) heartbeat() {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			_ = c.send(receiverID, NamespaceHeartbeat, map[string]any{"type": "PING"})
		case <-c.done:
			return
		}
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
}
