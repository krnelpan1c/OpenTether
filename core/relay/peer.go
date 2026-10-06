package relay

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/session"
)

type peerSession struct {
	srv        *Server
	conn       *quic.Conn
	log        *slog.Logger
	clientName string
	sender     *session.DatagramSender

	mu    sync.Mutex
	flows map[uint64]*udpFlow
}

func (ss *peerSession) run() {
	ctx := ss.conn.Context()
	go ss.acceptUniStreams(ctx)
	go session.ReceiveDatagrams(ss.conn, ss.handleDatagram)
	go ss.reapFlows(ctx)
	for {
		st, err := ss.conn.AcceptStream(ctx)
		if err != nil {
			break
		}
		go ss.handleStream(ctx, st)
	}
	ss.mu.Lock()
	for id, f := range ss.flows {
		f.close()
		delete(ss.flows, id)
	}
	ss.mu.Unlock()
}

func (ss *peerSession) handleStream(ctx context.Context, st *quic.Stream) {
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	var kind [1]byte
	if _, err := io.ReadFull(st, kind[:]); err != nil {
		st.CancelRead(proto.ErrCodeProtocol)
		st.CancelWrite(proto.ErrCodeProtocol)
		return
	}
	switch kind[0] {
	case proto.StreamTCP:
		ss.handleTCP(ctx, st)
	case proto.StreamDNS:
		ss.handleDNS(ctx, st)
	default:
		st.CancelRead(proto.ErrCodeProtocol)
		st.CancelWrite(proto.ErrCodeProtocol)
	}
}

func (ss *peerSession) acceptUniStreams(ctx context.Context) {
	for {
		rs, err := ss.conn.AcceptUniStream(ctx)
		if err != nil {
			return
		}
		go func() {
			var kind [1]byte
			if _, err := io.ReadFull(rs, kind[:]); err != nil || kind[0] != proto.StreamUDPOverflow {
				rs.CancelRead(proto.ErrCodeProtocol)
				return
			}
			session.ReadOverflow(rs, ss.handleDatagram)
		}()
	}
}

// --- TCP -------------------------------------------------------------------

func (ss *peerSession) handleTCP(ctx context.Context, st *quic.Stream) {
	srv := ss.srv
	dst, err := proto.ReadAddrPort(st)
	if err != nil {
		st.CancelRead(proto.ErrCodeProtocol)
		st.CancelWrite(proto.ErrCodeProtocol)
		return
	}
	_ = st.SetReadDeadline(time.Time{})

	if dst.Addr().Is6() && !dst.Addr().Is4In6() && !srv.hasIPv6() {
		ss.rejectTCP(st, proto.ResultNetworkUnreachable)
		return
	}
	c, err := srv.dialer().DialContext(ctx, "tcp", dst.String())
	if err != nil {
		srv.errs.Add(1)
		ss.log.Debug("tcp dial failed", "dst", dst, "err", err)
		ss.rejectTCP(st, resultFor(err))
		return
	}
	if _, err := st.Write([]byte{proto.ResultOK}); err != nil {
		c.Close()
		return
	}
	srv.tcpTotal.Add(1)
	srv.tcpActive.Add(1)
	defer srv.tcpActive.Add(-1)
	pipe(st, c.(*net.TCPConn), &srv.bytesUp, &srv.bytesDown)
}

func (ss *peerSession) rejectTCP(st *quic.Stream, code byte) {
	_, _ = st.Write([]byte{code})
	_ = st.Close()
	st.CancelRead(proto.ErrCodeNone)
}

// pipe copies between a QUIC stream and a TCP socket in both directions,
// propagating half-closes (FIN) and resets.
func pipe(st *quic.Stream, c *net.TCPConn, up, down *atomic.Int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // computer → internet
		defer wg.Done()
		_, err := io.Copy(&countingWriter{w: c, n: up}, st)
		if err != nil {
			_ = c.SetLinger(0) // abort with RST
			_ = c.Close()
			return
		}
		_ = c.CloseWrite()
	}()
	go func() { // internet → computer
		defer wg.Done()
		_, err := io.Copy(&countingWriter{w: st, n: down}, c)
		if err != nil {
			st.CancelWrite(proto.ErrCodeConnReset)
			st.CancelRead(proto.ErrCodeConnReset)
			return
		}
		_ = st.Close()
	}()
	wg.Wait()
	_ = c.Close()
	st.CancelRead(proto.ErrCodeNone)
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n.Add(int64(n))
	return n, err
}

// --- DNS -------------------------------------------------------------------

func (ss *peerSession) handleDNS(ctx context.Context, st *quic.Stream) {
	defer st.CancelRead(proto.ErrCodeNone)
	q, err := proto.ReadFrame(st, proto.MaxDNSMessage)
	if err != nil {
		st.CancelWrite(proto.ErrCodeProtocol)
		return
	}
	ss.srv.dnsQueries.Add(1)
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	ans, err := ss.srv.resolve(rctx, q)
	cancel()
	if err != nil {
		ss.srv.errs.Add(1)
		ss.log.Debug("dns query failed", "err", err)
		ans = nil
	}
	_ = proto.WriteFrame(st, ans)
	_ = st.Close()
}

// --- UDP -------------------------------------------------------------------

// udpFlow is one phone UDP socket serving one desktop source endpoint. The
// socket sends to any destination and accepts replies from any address,
// giving desktop apps endpoint-independent ("full cone") behaviour on our
// side of the carrier NAT.
type udpFlow struct {
	id       uint64
	pc       net.PacketConn
	lastUsed atomic.Int64 // unix nanos
	dnsOnly  atomic.Bool  // true while the flow only talked to port 53
	once     sync.Once
}

func (f *udpFlow) touch() { f.lastUsed.Store(time.Now().UnixNano()) }

func (f *udpFlow) close() { f.once.Do(func() { f.pc.Close() }) }

func (ss *peerSession) handleDatagram(d proto.Datagram) {
	srv := ss.srv
	dst := d.Addr
	if dst.Addr().Is6() && !srv.hasIPv6() {
		return
	}
	f, err := ss.flow(d.FlowID, dst.Port() == 53)
	if err != nil {
		srv.errs.Add(1)
		ss.log.Debug("udp socket failed", "err", err)
		return
	}
	f.touch()
	if dst.Port() != 53 {
		f.dnsOnly.Store(false)
	}
	if _, err := f.pc.WriteTo(d.Payload, net.UDPAddrFromAddrPort(dst)); err == nil {
		srv.bytesUp.Add(int64(len(d.Payload)))
	}
}

func (ss *peerSession) flow(id uint64, dns bool) (*udpFlow, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if f, ok := ss.flows[id]; ok {
		return f, nil
	}
	if len(ss.flows) >= ss.srv.cfg.MaxUDPFlows {
		ss.evictOldestLocked()
	}
	pc, err := ss.srv.listenUDP(ss.conn.Context())
	if err != nil {
		return nil, err
	}
	f := &udpFlow{id: id, pc: pc}
	f.touch()
	f.dnsOnly.Store(dns)
	ss.flows[id] = f
	ss.srv.udpFlows.Add(1)
	go ss.readFlow(f)
	return f, nil
}

func (ss *peerSession) readFlow(f *udpFlow) {
	defer func() {
		f.close()
		ss.mu.Lock()
		if ss.flows[f.id] == f {
			delete(ss.flows, f.id)
		}
		ss.mu.Unlock()
		ss.srv.udpFlows.Add(-1)
	}()
	buf := make([]byte, 0xFFFF)
	for {
		n, addr, err := f.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ua, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		f.touch()
		src := ua.AddrPort()
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		ss.srv.bytesDown.Add(int64(n))
		if err := ss.sender.Send(proto.Datagram{FlowID: f.id, Addr: src, Payload: buf[:n]}); err != nil {
			if ss.conn.Context().Err() != nil {
				return
			}
		}
	}
}

func (ss *peerSession) evictOldestLocked() {
	var oldest *udpFlow
	for _, f := range ss.flows {
		if oldest == nil || f.lastUsed.Load() < oldest.lastUsed.Load() {
			oldest = f
		}
	}
	if oldest != nil {
		delete(ss.flows, oldest.id)
		oldest.close()
	}
}

func (ss *peerSession) reapFlows(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			idle := ss.srv.cfg.UDPIdleTimeout
			ss.mu.Lock()
			for id, f := range ss.flows {
				limit := idle
				if f.dnsOnly.Load() {
					limit = 15 * time.Second
				}
				if now.Sub(time.Unix(0, f.lastUsed.Load())) > limit {
					delete(ss.flows, id)
					f.close()
				}
			}
			ss.mu.Unlock()
		}
	}
}
