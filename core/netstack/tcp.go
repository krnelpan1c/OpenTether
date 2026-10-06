package netstack

import (
	"context"
	"io"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

// handleTCP runs (in its own goroutine) for every new TCP connection the
// desktop opens. The upstream connection is dialed before the handshake
// completes, so unreachable hosts get a RST instead of a hanging connect.
func (s *Stack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := netip.AddrPortFrom(toAddr(id.LocalAddress), id.LocalPort)

	if s.isDNS(dst.Addr()) {
		if dst.Port() != 53 {
			r.Complete(true)
			return
		}
		if conn, _ := s.accept(r); conn != nil {
			s.serveDNSTCP(conn)
		}
		return
	}

	up := s.upstream()
	if up == nil {
		r.Complete(true)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	remote, err := up.DialTCP(ctx, dst)
	cancel()
	if err != nil {
		s.log.Debug("tcp connect failed", "dst", dst, "err", err)
		r.Complete(true)
		return
	}
	local, ep := s.accept(r)
	if local == nil {
		remote.Abort()
		return
	}
	s.tcpActive.Add(1)
	defer s.tcpActive.Add(-1)
	s.relayTCP(local, ep, remote)
}

func (s *Stack) accept(r *tcp.ForwarderRequest) (*gonet.TCPConn, tcpip.Endpoint) {
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		return nil, nil
	}
	r.Complete(false)
	ep.SocketOptions().SetKeepAlive(true)
	return gonet.NewTCPConn(&wq, ep), ep
}

// relayTCP copies between the desktop-side connection and the proxied
// upstream connection, mirroring half-closes and resets.
func (s *Stack) relayTCP(local *gonet.TCPConn, ep tcpip.Endpoint, remote TCPConn) {
	abortLocal := func() {
		ep.SocketOptions().SetLinger(tcpip.LingerOption{Enabled: true, Timeout: 0})
		_ = local.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // desktop → phone
		defer wg.Done()
		if _, err := io.Copy(&countingWriter{w: remote, n: &s.bytesUp}, local); err != nil {
			remote.Abort()
			return
		}
		_ = remote.CloseWrite()
	}()
	go func() { // phone → desktop
		defer wg.Done()
		if _, err := io.Copy(&countingWriter{w: local, n: &s.bytesDown}, remote); err != nil {
			abortLocal()
			return
		}
		_ = local.CloseWrite()
	}()
	wg.Wait()
	_ = local.Close()
	_ = remote.Close()
}

// serveDNSTCP answers DNS-over-TCP queries sent to the virtual resolver.
func (s *Stack) serveDNSTCP(conn *gonet.TCPConn) {
	defer conn.Close()
	for {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		q, err := proto.ReadFrame(conn, proto.MaxDNSMessage)
		if err != nil {
			return
		}
		ans := s.resolve(q)
		if ans == nil {
			return
		}
		if err := proto.WriteFrame(conn, ans); err != nil {
			return
		}
	}
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
