// Package proto defines the OpenTether wire protocol that runs on top of a
// QUIC connection between the desktop client and the phone relay.
//
// Layout of a session:
//
//   - The client opens one bidirectional control stream, writes a Hello and
//     reads a HelloAck. The control stream stays open for the session's
//     lifetime; closing it ends the session.
//   - Every proxied TCP connection is its own bidirectional stream that starts
//     with StreamTCP followed by the destination address. The server answers
//     with a single result byte and then both directions carry raw bytes.
//   - Every DNS query is its own bidirectional stream (StreamDNS) carrying one
//     length-prefixed query and one length-prefixed answer.
//   - UDP payloads travel as QUIC datagrams (see Datagram). Payloads that do
//     not fit in a datagram are sent on a lazily-opened unidirectional
//     StreamUDPOverflow stream as length-prefixed Datagram records.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
)

const (
	// Version is the protocol version negotiated in Hello.
	Version = 1
	// ALPN is the TLS application protocol identifier.
	ALPN = "opentether/1"
	// DefaultWifiPort is the UDP port the phone listens on in Wi-Fi Direct mode.
	DefaultWifiPort = 47100
	// AbstractSocketName is the abstract Unix socket the phone listens on for
	// USB (adb forward) connections.
	AbstractSocketName = "opentether"
	// MaxDNSMessage bounds DNS messages carried over StreamDNS.
	MaxDNSMessage = 65535
)

// Stream types are the first byte written on a client-opened stream.
const (
	StreamControl     byte = 1
	StreamTCP         byte = 2
	StreamDNS         byte = 3
	StreamUDPOverflow byte = 4
)

// Result codes returned by the server after a StreamTCP header.
const (
	ResultOK                 byte = 0
	ResultGeneralFailure     byte = 1
	ResultNetworkUnreachable byte = 2
	ResultHostUnreachable    byte = 3
	ResultConnectionRefused  byte = 4
	ResultTimeout            byte = 5
	ResultNotAllowed         byte = 6
)

// Application error codes used when closing QUIC connections and streams.
const (
	ErrCodeNone         = 0
	ErrCodeProtocol     = 1
	ErrCodeUnauthorized = 2
	ErrCodeShutdown     = 3
	ErrCodeConnReset    = 4
)

// ResultError is returned by clients when the server rejected a TCP open.
type ResultError byte

func (e ResultError) Error() string {
	switch byte(e) {
	case ResultNetworkUnreachable:
		return "network unreachable"
	case ResultHostUnreachable:
		return "host unreachable"
	case ResultConnectionRefused:
		return "connection refused"
	case ResultTimeout:
		return "connection timed out"
	case ResultNotAllowed:
		return "not allowed"
	default:
		return "general failure"
	}
}

// Hello is sent by the client on the control stream.
type Hello struct {
	Version    int    `json:"version"`
	Token      string `json:"token,omitempty"`
	ClientName string `json:"client"`
}

// HelloAck is the server's answer to Hello.
type HelloAck struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Version    int    `json:"version"`
	ServerName string `json:"server"`
	IPv6       bool   `json:"ipv6"`
}

// WriteJSON writes v as a single length-prefixed JSON record.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFrame(w, b)
}

// ReadJSON reads a single length-prefixed JSON record into v.
func ReadJSON(r io.Reader, v any) error {
	b, err := ReadFrame(r, 16<<10)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteFrame writes a 2-byte big-endian length followed by b.
func WriteFrame(w io.Writer, b []byte) error {
	if len(b) > 0xFFFF {
		return fmt.Errorf("proto: frame too large (%d bytes)", len(b))
	}
	buf := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(buf, uint16(len(b)))
	copy(buf[2:], b)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads a frame written by WriteFrame. Frames larger than max are
// rejected.
func ReadFrame(r io.Reader, max int) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > max {
		return nil, fmt.Errorf("proto: frame of %d bytes exceeds limit %d", n, max)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

const (
	familyIPv4 byte = 4
	familyIPv6 byte = 6
)

// ErrBadAddress is returned when an encoded address cannot be decoded.
var ErrBadAddress = errors.New("proto: malformed address")

// AppendAddrPort appends the wire encoding of ap: a family byte, the raw
// address and a big-endian port.
func AppendAddrPort(b []byte, ap netip.AddrPort) []byte {
	addr := ap.Addr().Unmap()
	if addr.Is4() {
		b = append(b, familyIPv4)
		a := addr.As4()
		b = append(b, a[:]...)
	} else {
		b = append(b, familyIPv6)
		a := addr.As16()
		b = append(b, a[:]...)
	}
	return binary.BigEndian.AppendUint16(b, ap.Port())
}

// ParseAddrPort decodes an address produced by AppendAddrPort and returns it
// together with the number of bytes consumed.
func ParseAddrPort(b []byte) (netip.AddrPort, int, error) {
	if len(b) < 1 {
		return netip.AddrPort{}, 0, ErrBadAddress
	}
	switch b[0] {
	case familyIPv4:
		if len(b) < 1+4+2 {
			return netip.AddrPort{}, 0, ErrBadAddress
		}
		addr := netip.AddrFrom4([4]byte(b[1:5]))
		return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(b[5:7])), 7, nil
	case familyIPv6:
		if len(b) < 1+16+2 {
			return netip.AddrPort{}, 0, ErrBadAddress
		}
		addr := netip.AddrFrom16([16]byte(b[1:17]))
		return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(b[17:19])), 19, nil
	default:
		return netip.AddrPort{}, 0, ErrBadAddress
	}
}

// ReadAddrPort reads one encoded address from r.
func ReadAddrPort(r io.Reader) (netip.AddrPort, error) {
	var fam [1]byte
	if _, err := io.ReadFull(r, fam[:]); err != nil {
		return netip.AddrPort{}, err
	}
	var size int
	switch fam[0] {
	case familyIPv4:
		size = 4 + 2
	case familyIPv6:
		size = 16 + 2
	default:
		return netip.AddrPort{}, ErrBadAddress
	}
	buf := make([]byte, 1+size)
	buf[0] = fam[0]
	if _, err := io.ReadFull(r, buf[1:]); err != nil {
		return netip.AddrPort{}, err
	}
	ap, _, err := ParseAddrPort(buf)
	return ap, err
}

// Datagram is a single UDP payload belonging to a flow.
//
// Flow IDs are allocated by the client, one per local UDP source endpoint.
// For client→server datagrams Addr is the destination; for server→client
// datagrams it is the remote source the payload came from.
type Datagram struct {
	FlowID  uint64
	Addr    netip.AddrPort
	Payload []byte
}

// MaxDatagramHeader is the largest possible encoded Datagram header.
const MaxDatagramHeader = binary.MaxVarintLen64 + 19

// AppendDatagram appends the wire encoding of d to b.
func AppendDatagram(b []byte, d Datagram) []byte {
	b = binary.AppendUvarint(b, d.FlowID)
	b = AppendAddrPort(b, d.Addr)
	return append(b, d.Payload...)
}

// ParseDatagram decodes a Datagram. The returned payload aliases b.
func ParseDatagram(b []byte) (Datagram, error) {
	id, n := binary.Uvarint(b)
	if n <= 0 {
		return Datagram{}, ErrBadAddress
	}
	ap, m, err := ParseAddrPort(b[n:])
	if err != nil {
		return Datagram{}, err
	}
	return Datagram{FlowID: id, Addr: ap, Payload: b[n+m:]}, nil
}
