package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

// SOCKS5 (RFC 1928) for proxy-only mode. No authentication: the proxy only
// listens on the Wi-Fi Direct address, which already needs the group
// password.

const (
	socksVersion       = 5
	socksNoAuth        = 0
	socksNoAcceptable  = 0xFF
	socksCmdConnect    = 1
	socksCmdUDP        = 3
	socksAtypIPv4      = 1
	socksAtypDomain    = 3
	socksAtypIPv6      = 4
	socksSucceeded     = 0
	socksGeneral       = 1
	socksNetUnreach    = 3
	socksHostUnreach   = 4
	socksRefused       = 5
	socksTTLExpired    = 6
	socksCmdNotSupport = 7
	socksAtypNotSupp   = 8
)

var errSocksAddr = errors.New("socks: unsupported address type")

func (s *Server) serveSOCKS(ctx context.Context, c *bufferedConn) {
	// Greeting: VER NMETHODS METHODS...
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if !containsByte(methods, socksNoAuth) {
		_, _ = c.Write([]byte{socksVersion, socksNoAcceptable})
		return
	}
	if _, err := c.Write([]byte{socksVersion, socksNoAuth}); err != nil {
		return
	}

	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	var req [3]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != socksVersion {
		return
	}
	host, port, err := readSocksAddr(c)
	if err != nil {
		writeSocksReply(c, socksAtypNotSupp, netip.AddrPort{})
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	switch req[1] {
	case socksCmdConnect:
		remote, err := s.dialHost(ctx, net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			s.errs.Add(1)
			s.log.Debug("socks CONNECT failed", "host", host, "err", err)
			writeSocksReply(c, socksReplyFor(err), netip.AddrPort{})
			return
		}
		bound, _ := netip.ParseAddrPort(remote.LocalAddr().String())
		writeSocksReply(c, socksSucceeded, bound)
		s.relayProxyTCP(c, remote)
	case socksCmdUDP:
		s.serveSOCKSUDP(ctx, c)
	default:
		writeSocksReply(c, socksCmdNotSupport, netip.AddrPort{})
	}
}

// serveSOCKSUDP handles UDP ASSOCIATE: the client sends SOCKS-wrapped
// datagrams to a socket we open next to the proxy port, and we relay them
// through an upstream socket. The association lives as long as the TCP
// control connection.
func (s *Server) serveSOCKSUDP(ctx context.Context, c *bufferedConn) {
	local, err := netip.ParseAddrPort(c.LocalAddr().String())
	if err != nil {
		writeSocksReply(c, socksGeneral, netip.AddrPort{})
		return
	}
	clientIP, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		writeSocksReply(c, socksGeneral, netip.AddrPort{})
		return
	}
	front, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(local.Addr(), 0)))
	if err != nil {
		writeSocksReply(c, socksGeneral, netip.AddrPort{})
		return
	}
	defer front.Close()
	upstream, err := s.listenUDP(ctx)
	if err != nil {
		writeSocksReply(c, socksGeneral, netip.AddrPort{})
		return
	}
	defer upstream.Close()

	bound := netip.MustParseAddrPort(front.LocalAddr().String())
	writeSocksReply(c, socksSucceeded, bound)
	s.udpFlows.Add(1)
	defer s.udpFlows.Add(-1)

	// The association ends when the client closes the control connection.
	go func() {
		_, _ = io.Copy(io.Discard, c)
		front.Close()
		upstream.Close()
	}()

	var clientAddr atomicAddrPort
	go func() { // internet → device
		buf := make([]byte, 0xFFFF)
		for {
			n, from, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			dst, ok := clientAddr.Load()
			ua, isUDP := from.(*net.UDPAddr)
			if !ok || !isUDP {
				continue
			}
			src := ua.AddrPort()
			src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
			s.bytesDown.Add(int64(n))
			pkt := appendSocksUDPHeader(make([]byte, 0, 22+n), src)
			_, _ = front.WriteToUDPAddrPort(append(pkt, buf[:n]...), dst)
		}
	}()

	buf := make([]byte, 0xFFFF)
	for { // device → internet
		n, from, err := front.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if from.Addr().Unmap() != clientIP.Addr().Unmap() {
			continue // only the client that opened the association
		}
		clientAddr.Store(from)
		host, port, payload, err := parseSocksUDP(buf[:n])
		if err != nil {
			continue
		}
		addrs, err := s.lookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			continue
		}
		if _, err := upstream.WriteTo(payload, net.UDPAddrFromAddrPort(netip.AddrPortFrom(addrs[0], port))); err == nil {
			s.bytesUp.Add(int64(len(payload)))
		}
	}
}

func readSocksAddr(r io.Reader) (host string, port uint16, err error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return "", 0, err
	}
	switch atyp[0] {
	case socksAtypIPv4:
		var b [4]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = netip.AddrFrom4(b).String()
	case socksAtypIPv6:
		var b [16]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", 0, err
		}
		host = netip.AddrFrom16(b).String()
	case socksAtypDomain:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", 0, err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(r, b); err != nil {
			return "", 0, err
		}
		host = string(b)
	default:
		return "", 0, errSocksAddr
	}
	var p [2]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return "", 0, err
	}
	return host, binary.BigEndian.Uint16(p[:]), nil
}

// parseSocksUDP parses RSV(2) FRAG(1) ATYP DST.ADDR DST.PORT DATA.
// Fragmented datagrams are not supported.
func parseSocksUDP(b []byte) (host string, port uint16, payload []byte, err error) {
	if len(b) < 4 || b[2] != 0 {
		return "", 0, nil, errSocksAddr
	}
	r := &sliceReader{b: b[3:]}
	host, port, err = readSocksAddr(r)
	if err != nil {
		return "", 0, nil, err
	}
	return host, port, r.b, nil
}

func appendSocksUDPHeader(b []byte, src netip.AddrPort) []byte {
	b = append(b, 0, 0, 0)
	return appendSocksAddr(b, src)
}

func appendSocksAddr(b []byte, ap netip.AddrPort) []byte {
	a := ap.Addr().Unmap()
	switch {
	case !a.IsValid():
		b = append(b, socksAtypIPv4, 0, 0, 0, 0)
	case a.Is4():
		v := a.As4()
		b = append(append(b, socksAtypIPv4), v[:]...)
	default:
		v := a.As16()
		b = append(append(b, socksAtypIPv6), v[:]...)
	}
	return binary.BigEndian.AppendUint16(b, ap.Port())
}

func writeSocksReply(w io.Writer, rep byte, bound netip.AddrPort) {
	_, _ = w.Write(appendSocksAddr([]byte{socksVersion, rep, 0}, bound))
}

func socksReplyFor(err error) byte {
	switch resultFor(err) {
	case proto.ResultNetworkUnreachable:
		return socksNetUnreach
	case proto.ResultHostUnreachable:
		return socksHostUnreach
	case proto.ResultConnectionRefused:
		return socksRefused
	case proto.ResultTimeout:
		return socksTTLExpired
	default:
		return socksGeneral
	}
}

func containsByte(b []byte, v byte) bool {
	for _, x := range b {
		if x == v {
			return true
		}
	}
	return false
}

type sliceReader struct{ b []byte }

func (r *sliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// atomicAddrPort holds the UDP address of the SOCKS client once known.
type atomicAddrPort struct {
	p atomic.Pointer[netip.AddrPort]
}

func (a *atomicAddrPort) Store(ap netip.AddrPort) { a.p.Store(&ap) }

func (a *atomicAddrPort) Load() (netip.AddrPort, bool) {
	p := a.p.Load()
	if p == nil {
		return netip.AddrPort{}, false
	}
	return *p, true
}
