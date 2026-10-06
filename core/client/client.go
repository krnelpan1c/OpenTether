// Package client implements the desktop end of an OpenTether session: it
// dials the phone relay over any packet link and exposes TCP, UDP and DNS
// primitives to the desktop network stack.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/session"
)

// Options configures Dial.
type Options struct {
	// Token is the pairing token; required for Wi-Fi Direct links.
	Token string
	// ClientName identifies this computer in the phone UI.
	ClientName string
	// Pin is the expected server certificate fingerprint. Empty accepts any
	// certificate; read Client.Fingerprint afterwards to pin it.
	Pin string
	// Reliable must be true for stream-based links (adb, USB accessory).
	Reliable bool
	// OnUDP receives UDP payloads from the phone. The payload is only valid
	// for the duration of the call.
	OnUDP  func(proto.Datagram)
	Logger *slog.Logger
}

// Client is an established session with a phone relay.
type Client struct {
	pc     net.PacketConn
	tr     *quic.Transport
	conn   *quic.Conn
	sender *session.DatagramSender
	info   proto.HelloAck
	fp     string
	log    *slog.Logger
}

// Dial performs the QUIC handshake and Hello exchange over pc. On success
// the Client owns pc; on failure pc is closed.
func Dial(ctx context.Context, pc net.PacketConn, remote net.Addr, opts Options) (*Client, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	c := &Client{pc: pc, tr: &quic.Transport{Conn: pc}, log: opts.Logger}
	conn, err := c.tr.Dial(ctx, remote, session.ClientTLSConfig(opts.Pin, &c.fp), session.QUICConfig(opts.Reliable))
	if err != nil {
		c.closeTransport()
		return nil, fmt.Errorf("handshake with phone failed: %w", err)
	}
	c.conn = conn
	c.sender = session.NewDatagramSender(conn)

	if err := c.hello(ctx, opts); err != nil {
		_ = conn.CloseWithError(proto.ErrCodeProtocol, "hello failed")
		c.closeTransport()
		return nil, err
	}

	onUDP := opts.OnUDP
	if onUDP == nil {
		onUDP = func(proto.Datagram) {}
	}
	go session.ReceiveDatagrams(conn, onUDP)
	go c.acceptUniStreams(onUDP)
	return c, nil
}

func (c *Client) hello(ctx context.Context, opts Options) error {
	control, err := c.conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	if _, err := control.Write([]byte{proto.StreamControl}); err != nil {
		return err
	}
	hello := proto.Hello{Version: proto.Version, Token: opts.Token, ClientName: opts.ClientName}
	if err := proto.WriteJSON(control, hello); err != nil {
		return err
	}
	_ = control.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := proto.ReadJSON(control, &c.info); err != nil {
		return fmt.Errorf("phone did not answer hello: %w", err)
	}
	_ = control.SetReadDeadline(time.Time{})
	if !c.info.OK {
		return fmt.Errorf("phone refused the connection: %s", c.info.Error)
	}
	go func() {
		// The phone closes the control stream to end the session.
		_, _ = io.Copy(io.Discard, control)
		_ = c.conn.CloseWithError(proto.ErrCodeNone, "")
	}()
	return nil
}

func (c *Client) acceptUniStreams(onUDP func(proto.Datagram)) {
	ctx := c.conn.Context()
	for {
		rs, err := c.conn.AcceptUniStream(ctx)
		if err != nil {
			return
		}
		go func() {
			var kind [1]byte
			if _, err := io.ReadFull(rs, kind[:]); err != nil || kind[0] != proto.StreamUDPOverflow {
				rs.CancelRead(proto.ErrCodeProtocol)
				return
			}
			session.ReadOverflow(rs, onUDP)
		}()
	}
}

// Info returns the phone's HelloAck.
func (c *Client) Info() proto.HelloAck { return c.info }

// Fingerprint returns the phone's certificate fingerprint.
func (c *Client) Fingerprint() string { return c.fp }

// Done is closed when the session ends.
func (c *Client) Done() <-chan struct{} { return c.conn.Context().Done() }

// Err returns why the session ended, or nil while it is alive.
func (c *Client) Err() error { return context.Cause(c.conn.Context()) }

// Close ends the session and closes the underlying link.
func (c *Client) Close() error {
	err := c.conn.CloseWithError(proto.ErrCodeNone, "client closing")
	c.closeTransport()
	return err
}

// closeTransport closes the link first: Transport.Close waits for its
// reader to stop, and closing the link is what reliably unblocks it on
// links without working read deadlines.
func (c *Client) closeTransport() {
	_ = c.pc.Close()
	_ = c.tr.Close()
}

// DialTCP asks the phone to open a TCP connection to dst.
func (c *Client) DialTCP(ctx context.Context, dst netip.AddrPort) (*Stream, error) {
	st, err := c.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	hdr := proto.AppendAddrPort([]byte{proto.StreamTCP}, dst)
	if _, err := st.Write(hdr); err != nil {
		st.CancelRead(proto.ErrCodeNone)
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetReadDeadline(dl)
	} else {
		_ = st.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	var res [1]byte
	if _, err := io.ReadFull(st, res[:]); err != nil {
		st.CancelRead(proto.ErrCodeNone)
		st.CancelWrite(proto.ErrCodeNone)
		return nil, err
	}
	_ = st.SetReadDeadline(time.Time{})
	if res[0] != proto.ResultOK {
		st.CancelRead(proto.ErrCodeNone)
		_ = st.Close()
		return nil, proto.ResultError(res[0])
	}
	return &Stream{st: st}, nil
}

// SendUDP sends one UDP payload. It is safe for concurrent use.
func (c *Client) SendUDP(d proto.Datagram) error { return c.sender.Send(d) }

// ResolveRaw sends a raw DNS query to the phone's resolver.
func (c *Client) ResolveRaw(ctx context.Context, query []byte) ([]byte, error) {
	st, err := c.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer st.CancelRead(proto.ErrCodeNone)
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}
	if _, err := st.Write([]byte{proto.StreamDNS}); err != nil {
		return nil, err
	}
	if err := proto.WriteFrame(st, query); err != nil {
		return nil, err
	}
	_ = st.Close()
	ans, err := proto.ReadFrame(st, proto.MaxDNSMessage)
	if err != nil {
		return nil, err
	}
	if len(ans) == 0 {
		return nil, errors.New("phone resolver failed")
	}
	return ans, nil
}

// Stream is a proxied TCP connection.
type Stream struct {
	st *quic.Stream
}

func (s *Stream) Read(p []byte) (int, error)  { return s.st.Read(p) }
func (s *Stream) Write(p []byte) (int, error) { return s.st.Write(p) }

// CloseWrite sends FIN to the remote host.
func (s *Stream) CloseWrite() error { return s.st.Close() }

// Abort resets the connection in both directions.
func (s *Stream) Abort() {
	s.st.CancelWrite(proto.ErrCodeConnReset)
	s.st.CancelRead(proto.ErrCodeConnReset)
}

// Close releases the stream after both directions finished.
func (s *Stream) Close() error {
	s.st.CancelRead(proto.ErrCodeNone)
	return s.st.Close()
}
