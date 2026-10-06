package session

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

// QUICConfig returns the QUIC configuration for a link. reliable is true for
// stream-based links (adb, USB accessory) where packets are never lost or
// reordered; there path MTU discovery is pointless and the largest packet
// size is used from the start.
func QUICConfig(reliable bool) *quic.Config {
	c := &quic.Config{
		HandshakeIdleTimeout:           10 * time.Second,
		MaxIdleTimeout:                 30 * time.Second,
		KeepAlivePeriod:                5 * time.Second,
		InitialStreamReceiveWindow:     1 << 20,
		MaxStreamReceiveWindow:         16 << 20,
		InitialConnectionReceiveWindow: 4 << 20,
		MaxConnectionReceiveWindow:     64 << 20,
		MaxIncomingStreams:             8192,
		MaxIncomingUniStreams:          16,
		EnableDatagrams:                true,
	}
	if reliable {
		c.InitialPacketSize = 1452
		c.DisablePathMTUDiscovery = true
	}
	return c
}

// DatagramSender sends proto.Datagrams over a QUIC connection, falling back
// to a lazily-opened unidirectional overflow stream when a payload does not
// fit in a QUIC DATAGRAM frame.
type DatagramSender struct {
	conn *quic.Conn

	mu       sync.Mutex
	overflow *quic.SendStream
	buf      []byte
}

// NewDatagramSender returns a sender for conn.
func NewDatagramSender(conn *quic.Conn) *DatagramSender {
	return &DatagramSender{conn: conn}
}

// Send transmits d. The payload is copied before Send returns.
func (s *DatagramSender) Send(d proto.Datagram) error {
	b := proto.AppendDatagram(make([]byte, 0, proto.MaxDatagramHeader+len(d.Payload)), d)
	err := s.conn.SendDatagram(b)
	var tooLarge *quic.DatagramTooLargeError
	if errors.As(err, &tooLarge) {
		return s.sendOverflow(b)
	}
	return err
}

func (s *DatagramSender) sendOverflow(record []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow == nil {
		st, err := s.conn.OpenUniStream()
		if err != nil {
			return err
		}
		if _, err := st.Write([]byte{proto.StreamUDPOverflow}); err != nil {
			st.CancelWrite(proto.ErrCodeProtocol)
			return err
		}
		s.overflow = st
	}
	if err := proto.WriteFrame(s.overflow, record); err != nil {
		s.overflow.CancelWrite(proto.ErrCodeProtocol)
		s.overflow = nil
		return err
	}
	return nil
}

// ReceiveDatagrams delivers every incoming QUIC datagram to fn until the
// connection closes. Malformed datagrams are dropped.
func ReceiveDatagrams(conn *quic.Conn, fn func(proto.Datagram)) {
	ctx := conn.Context()
	for {
		b, err := conn.ReceiveDatagram(ctx)
		if err != nil {
			return
		}
		if d, err := proto.ParseDatagram(b); err == nil {
			fn(d)
		}
	}
}

// ReadOverflow delivers datagram records from an overflow stream (after its
// type byte has been consumed) to fn until the stream ends.
func ReadOverflow(r io.Reader, fn func(proto.Datagram)) {
	for {
		b, err := proto.ReadFrame(r, 0xFFFF)
		if err != nil {
			return
		}
		if d, err := proto.ParseDatagram(b); err == nil {
			fn(d)
		}
	}
}
