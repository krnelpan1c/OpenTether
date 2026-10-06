package netstack

import (
	"context"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

const udpFlowIdle = 3 * time.Minute

// routeUDP forwards one UDP payload sent by a desktop app.
func (s *Stack) routeUDP(src, dst netip.AddrPort, data []byte) {
	if s.isDNS(dst.Addr()) {
		if dst.Port() == 53 {
			q := append([]byte(nil), data...)
			go s.serveDNSUDP(src, dst, q)
		}
		return
	}
	if !routable(dst.Addr()) {
		s.dropped.Add(1)
		return
	}
	up := s.upstream()
	if up == nil {
		s.dropped.Add(1)
		return
	}
	f := s.flowFor(src)
	f.touch()
	s.bytesUp.Add(int64(len(data)))
	if err := up.SendUDP(proto.Datagram{FlowID: f.id, Addr: dst, Payload: data}); err != nil {
		s.dropped.Add(1)
	}
}

// routable reports whether a destination makes sense to relay over the
// phone's mobile connection. Multicast, broadcast and link-local discovery
// chatter (SSDP, mDNS, LLMNR, ...) stays on the computer.
func routable(a netip.Addr) bool {
	return !(a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsUnspecified() ||
		a.IsLoopback() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}))
}

func (s *Stack) flowFor(src netip.AddrPort) *udpFlow {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	if f, ok := s.flowBySrc[src]; ok {
		return f
	}
	s.nextFlow++
	f := &udpFlow{id: s.nextFlow, src: src}
	s.flowBySrc[src] = f
	s.flowByID[f.id] = f
	return f
}

// HandleUDP delivers a UDP payload received from the phone to the desktop
// app that owns the flow. The payload is not retained.
func (s *Stack) HandleUDP(d proto.Datagram) {
	s.udpMu.Lock()
	f := s.flowByID[d.FlowID]
	s.udpMu.Unlock()
	if f == nil || f.src.Addr().Is4() != d.Addr.Addr().Is4() {
		s.dropped.Add(1)
		return
	}
	f.touch()
	s.bytesDown.Add(int64(len(d.Payload)))
	s.writePackets(buildUDP(d.Addr, f.src, d.Payload, s.cfg.MTU, s.ipID.Add(1)))
}

func (s *Stack) reapLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.udpMu.Lock()
			for id, f := range s.flowByID {
				if now.Sub(time.Unix(0, f.lastUsed.Load())) > udpFlowIdle {
					delete(s.flowByID, id)
					delete(s.flowBySrc, f.src)
				}
			}
			s.udpMu.Unlock()
		}
	}
}

// handleReassembledUDP receives UDP datagrams that arrived fragmented and
// were reassembled by gVisor.
func (s *Stack) handleReassembledUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	src := netip.AddrPortFrom(toAddr(id.RemoteAddress), id.RemotePort)
	dst := netip.AddrPortFrom(toAddr(id.LocalAddress), id.LocalPort)
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		return true
	}
	conn := gonet.NewUDPConn(&wq, ep)
	go func() {
		defer conn.Close()
		buf := make([]byte, 0xFFFF)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			s.routeUDP(src, dst, buf[:n])
		}
	}()
	return true
}

// serveDNSUDP answers a query sent to the virtual resolver.
func (s *Stack) serveDNSUDP(client, server netip.AddrPort, q []byte) {
	ans := s.resolve(q)
	if ans == nil {
		return
	}
	s.writePackets(buildUDP(server, client, ans, s.cfg.MTU, s.ipID.Add(1)))
}

// resolve forwards a raw query to the phone, answering SERVFAIL when that
// is impossible so clients fail fast instead of timing out.
func (s *Stack) resolve(q []byte) []byte {
	s.dnsQueries.Add(1)
	if up := s.upstream(); up != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		ans, err := up.ResolveRaw(ctx, q)
		cancel()
		if err == nil {
			return ans
		}
		s.log.Debug("dns query failed", "err", err)
	}
	return servfail(q)
}

// servfail turns a query into a SERVFAIL response.
func servfail(q []byte) []byte {
	if len(q) < 12 {
		return nil
	}
	r := append([]byte(nil), q...)
	r[2] = (r[2] | 0x80) &^ 0x04 // QR=1, AA=0
	r[3] = 0x80 | 0x02           // RA=1, RCODE=SERVFAIL
	clear(r[6:10])               // no answer or authority records
	return r
}
