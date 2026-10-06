// Package relay implements the phone side of OpenTether: it accepts QUIC
// sessions from desktop clients and turns their requests into ordinary
// sockets owned by the app, so traffic leaves the phone as the app's own.
package relay

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/session"
)

// Platform provides the OS integration the relay needs. On Android it is
// implemented in Kotlin; a nil Platform uses plain sockets.
type Platform interface {
	// BindSocket attaches a freshly created socket to the selected upstream
	// network (for example the cellular network).
	BindSocket(fd int) error
	// ResolveRaw sends a raw DNS query through the platform resolver and
	// returns the raw answer.
	ResolveRaw(ctx context.Context, query []byte) ([]byte, error)
	// HasIPv6 reports whether the upstream network has IPv6 connectivity.
	HasIPv6() bool
}

// Config configures a Server.
type Config struct {
	Platform   Platform
	Identity   tls.Certificate
	ServerName string
	// Token returns the current pairing token. Sessions on untrusted
	// listeners must present it.
	Token  func() string
	Logger *slog.Logger
	// MaxUDPFlows bounds the UDP sockets one session may hold.
	MaxUDPFlows int
	// UDPIdleTimeout closes UDP sockets without traffic for this long.
	UDPIdleTimeout time.Duration
	// FallbackDNS is used for DNS when Platform is nil.
	FallbackDNS string
}

// Stats is a snapshot of relay counters. Up means computer → internet.
type Stats struct {
	Sessions   int64    `json:"sessions"`
	Clients    []string `json:"clients"`
	TCPActive  int64    `json:"tcpActive"`
	TCPTotal   int64    `json:"tcpTotal"`
	UDPFlows   int64    `json:"udpFlows"`
	DNSQueries int64    `json:"dnsQueries"`
	BytesUp    int64    `json:"bytesUp"`
	BytesDown  int64    `json:"bytesDown"`
	Errors     int64    `json:"errors"`
}

// Server relays traffic for desktop clients.
type Server struct {
	cfg Config
	log *slog.Logger

	tcpActive, tcpTotal, udpFlows, dnsQueries atomic.Int64
	bytesUp, bytesDown, errs                  atomic.Int64

	mu       sync.Mutex
	sessions map[*peerSession]struct{}
	// proxyClients maps proxy-mode client IPs to when they were last seen.
	proxyClients map[netip.Addr]time.Time
}

// New returns a Server. It does not listen until one of the Serve methods is
// called.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxUDPFlows == 0 {
		cfg.MaxUDPFlows = 1024
	}
	if cfg.UDPIdleTimeout == 0 {
		cfg.UDPIdleTimeout = 2 * time.Minute
	}
	if cfg.FallbackDNS == "" {
		cfg.FallbackDNS = "1.1.1.1:53"
	}
	if cfg.Token == nil {
		cfg.Token = func() string { return "" }
	}
	return &Server{
		cfg:          cfg,
		log:          cfg.Logger,
		sessions:     make(map[*peerSession]struct{}),
		proxyClients: make(map[netip.Addr]time.Time),
	}
}

// Stats returns a snapshot of the relay counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	clients := make([]string, 0, len(s.sessions))
	for sess := range s.sessions {
		clients = append(clients, sess.clientName)
	}
	for ip, seen := range s.proxyClients {
		if time.Since(seen) > proxyClientTTL {
			delete(s.proxyClients, ip)
			continue
		}
		clients = append(clients, ip.String()+" (proxy)")
	}
	s.mu.Unlock()
	sort.Strings(clients)
	return Stats{
		Sessions:   int64(len(clients)),
		Clients:    clients,
		TCPActive:  s.tcpActive.Load(),
		TCPTotal:   s.tcpTotal.Load(),
		UDPFlows:   s.udpFlows.Load(),
		DNSQueries: s.dnsQueries.Load(),
		BytesUp:    s.bytesUp.Load(),
		BytesDown:  s.bytesDown.Load(),
		Errors:     s.errs.Load(),
	}
}

// CloseSessions ends every active session, e.g. after the pairing token was
// rotated.
func (s *Server) CloseSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sess := range s.sessions {
		_ = sess.conn.CloseWithError(proto.ErrCodeShutdown, "server shutting down")
	}
}

// ServeStream accepts byte-stream connections (adb forward, USB accessory)
// from ln and runs one QUIC session over each. trusted links skip the pairing
// token check because the link itself is authenticated (USB debugging
// authorization, physical cable). It returns when ln is closed or ctx ends.
func (s *Server) ServeStream(ctx context.Context, ln net.Listener, trusted bool) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go s.serveStreamConn(ctx, c, trusted)
	}
}

func (s *Server) serveStreamConn(ctx context.Context, c net.Conn, trusted bool) {
	_ = s.ServeLink(ctx, session.NewFramedPacketConn(c), trusted, 15*time.Second)
}

// ServeLink runs one QUIC session over a reliable point-to-point link (a
// framed socket or USB accessory) and closes pc when it ends. acceptTimeout
// bounds the wait for the handshake; zero waits until ctx ends.
func (s *Server) ServeLink(ctx context.Context, pc net.PacketConn, trusted bool, acceptTimeout time.Duration) error {
	tr := &quic.Transport{Conn: pc}
	defer func() {
		// Close the link, then let the transport wind down in the
		// background: on a USB accessory the reader may sit in a blocking
		// read until the next packet or the cable is unplugged, and
		// Transport.Close waits for it.
		_ = pc.Close()
		go tr.Close()
	}()
	ln, err := tr.Listen(session.ServerTLSConfig(s.cfg.Identity), session.QUICConfig(true))
	if err != nil {
		s.log.Warn("quic listen on link failed", "err", err)
		return err
	}
	defer ln.Close()
	actx, cancel := ctx, context.CancelFunc(func() {})
	if acceptTimeout > 0 {
		actx, cancel = context.WithTimeout(ctx, acceptTimeout)
	}
	qc, err := ln.Accept(actx)
	cancel()
	if err != nil {
		s.log.Debug("no QUIC handshake on link", "err", err)
		return err
	}
	s.handleConn(ctx, qc, trusted)
	return nil
}

// ServePacket runs QUIC on a datagram socket (Wi-Fi Direct). Sessions on it
// must present the pairing token unless trusted is set.
func (s *Server) ServePacket(ctx context.Context, pc net.PacketConn, trusted bool) error {
	tr := &quic.Transport{Conn: pc}
	defer tr.Close()
	ln, err := tr.Listen(session.ServerTLSConfig(s.cfg.Identity), session.QUICConfig(false))
	if err != nil {
		return err
	}
	defer ln.Close()
	for {
		qc, err := ln.Accept(ctx)
		if err != nil {
			return err
		}
		go s.handleConn(ctx, qc, trusted)
	}
}

func (s *Server) handleConn(ctx context.Context, qc *quic.Conn, trusted bool) {
	log := s.log.With("peer", qc.RemoteAddr().String())

	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	control, err := qc.AcceptStream(hctx)
	cancel()
	if err != nil {
		_ = qc.CloseWithError(proto.ErrCodeProtocol, "no control stream")
		return
	}
	_ = control.SetReadDeadline(time.Now().Add(10 * time.Second))
	var kind [1]byte
	var hello proto.Hello
	if _, err := io.ReadFull(control, kind[:]); err != nil || kind[0] != proto.StreamControl {
		_ = qc.CloseWithError(proto.ErrCodeProtocol, "expected control stream")
		return
	}
	if err := proto.ReadJSON(control, &hello); err != nil {
		_ = qc.CloseWithError(proto.ErrCodeProtocol, "bad hello")
		return
	}
	_ = control.SetReadDeadline(time.Time{})

	ack := proto.HelloAck{Version: proto.Version, ServerName: s.cfg.ServerName, IPv6: s.hasIPv6()}
	code := quic.ApplicationErrorCode(proto.ErrCodeNone)
	switch {
	case hello.Version != proto.Version:
		ack.Error = fmt.Sprintf("unsupported protocol version %d (phone speaks %d)", hello.Version, proto.Version)
		code = proto.ErrCodeProtocol
	case !trusted && !tokenOK(hello.Token, s.cfg.Token()):
		ack.Error = "pairing code rejected; scan the QR code on the phone again"
		code = proto.ErrCodeUnauthorized
	default:
		ack.OK = true
	}
	if err := proto.WriteJSON(control, ack); err != nil || !ack.OK {
		if ack.Error != "" {
			log.Warn("session rejected", "client", hello.ClientName, "reason", ack.Error)
		}
		time.Sleep(200 * time.Millisecond) // let the ack reach the client
		_ = qc.CloseWithError(code, ack.Error)
		return
	}

	sess := &peerSession{
		srv:        s,
		conn:       qc,
		log:        log.With("client", hello.ClientName),
		clientName: hello.ClientName,
		sender:     session.NewDatagramSender(qc),
		flows:      make(map[uint64]*udpFlow),
	}
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
	sess.log.Info("session started")

	go func() {
		// The session lives as long as the control stream.
		_, _ = io.Copy(io.Discard, control)
		_ = qc.CloseWithError(proto.ErrCodeNone, "")
	}()
	sess.run()

	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
	sess.log.Info("session ended", "reason", context.Cause(qc.Context()))
}

func tokenOK(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) hasIPv6() bool {
	if s.cfg.Platform == nil {
		return true
	}
	return s.cfg.Platform.HasIPv6()
}

// control returns a syscall.RawConn hook that binds sockets to the upstream
// network before they connect.
func (s *Server) control(_, _ string, c syscall.RawConn) error {
	if s.cfg.Platform == nil {
		return nil
	}
	var bindErr error
	if err := c.Control(func(fd uintptr) { bindErr = s.cfg.Platform.BindSocket(int(fd)) }); err != nil {
		return err
	}
	return bindErr
}

func (s *Server) dialer() *net.Dialer {
	return &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Control: s.control}
}

func (s *Server) listenUDP(ctx context.Context) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: s.control}
	return lc.ListenPacket(ctx, "udp", ":0")
}

// resolve answers a raw DNS query via the platform or the fallback resolver.
func (s *Server) resolve(ctx context.Context, q []byte) ([]byte, error) {
	if s.cfg.Platform != nil {
		return s.cfg.Platform.ResolveRaw(ctx, q)
	}
	c, err := s.dialer().DialContext(ctx, "udp", s.cfg.FallbackDNS)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, proto.MaxDNSMessage)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// resultFor maps a dial error to a protocol result code.
func resultFor(err error) byte {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return proto.ResultConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH):
		return proto.ResultNetworkUnreachable
	case errors.Is(err, syscall.EHOSTUNREACH):
		return proto.ResultHostUnreachable
	case errors.As(err, &ne) && ne.Timeout():
		return proto.ResultTimeout
	default:
		return proto.ResultGeneralFailure
	}
}
