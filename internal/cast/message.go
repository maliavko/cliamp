// Package cast talks to Google Cast devices, such as a Chromecast or an
// Android TV: it finds them on the local network and controls the receiver
// apps that run on them.
package cast

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Namespaces of the Cast protocol that the client uses.
const (
	NamespaceConnection = "urn:x-cast:com.google.cast.tp.connection"
	NamespaceHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	NamespaceReceiver   = "urn:x-cast:com.google.cast.receiver"
	NamespaceMedia      = "urn:x-cast:com.google.cast.media"
)

// receiverID is the destination of the device itself, as opposed to an app.
const receiverID = "receiver-0"

// maxMessage caps a frame. Real messages are a few kilobytes; a larger length
// means the stream is out of step.
const maxMessage = 1 << 20

// message is one CastMessage: a JSON payload addressed to a namespace.
type message struct {
	source      string
	destination string
	namespace   string
	payload     []byte
}

// The CastMessage protobuf fields. The client sends only string payloads.
const (
	fieldProtocolVersion = 1
	fieldSourceID        = 2
	fieldDestinationID   = 3
	fieldNamespace       = 4
	fieldPayloadType     = 5
	fieldPayloadUTF8     = 6
)

// encode frames m: a big-endian length, then the protobuf message. The
// protocol version and the payload type are both 0, CASTV2_1_0 and STRING.
func (m message) encode() []byte {
	var body []byte
	body = appendVarintField(body, fieldProtocolVersion, 0)
	body = appendBytesField(body, fieldSourceID, []byte(m.source))
	body = appendBytesField(body, fieldDestinationID, []byte(m.destination))
	body = appendBytesField(body, fieldNamespace, []byte(m.namespace))
	body = appendVarintField(body, fieldPayloadType, 0)
	body = appendBytesField(body, fieldPayloadUTF8, m.payload)
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(body)), uint32(len(body)))
	return append(frame, body...)
}

// readMessage reads one framed message from r.
func readMessage(r io.Reader) (message, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return message{}, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > maxMessage {
		return message{}, fmt.Errorf("cast: message of %d bytes", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return message{}, err
	}
	return decode(body)
}

var errMalformed = errors.New("cast: malformed message")

// decode reads the fields the client needs and skips the rest.
func decode(body []byte) (message, error) {
	var m message
	for len(body) > 0 {
		key, n := uvarint(body)
		if n <= 0 {
			return message{}, errMalformed
		}
		body = body[n:]
		field, wire := key>>3, key&7
		switch wire {
		case 0:
			_, n := uvarint(body)
			if n <= 0 {
				return message{}, errMalformed
			}
			body = body[n:]
		case 2:
			size, n := uvarint(body)
			if n <= 0 || size > uint64(len(body)-n) {
				return message{}, errMalformed
			}
			value := body[n : n+int(size)]
			body = body[n+int(size):]
			switch field {
			case fieldSourceID:
				m.source = string(value)
			case fieldDestinationID:
				m.destination = string(value)
			case fieldNamespace:
				m.namespace = string(value)
			case fieldPayloadUTF8:
				m.payload = value
			}
		default:
			return message{}, errMalformed
		}
	}
	return m, nil
}

func appendVarintField(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3)
	return binary.AppendUvarint(b, v)
}

func appendBytesField(b []byte, field int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func uvarint(b []byte) (uint64, int) {
	return binary.Uvarint(b)
}
