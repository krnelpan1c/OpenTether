package netstack

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

var (
	osAddr  = netip.MustParseAddr("172.19.0.1")
	dnsAddr = netip.MustParseAddr("172.19.0.2")
)

// osStack is a second gVisor stack that plays the desktop operating system:
// apps on it open sockets, and its packets flow through a fake TUN device
// into the Stack under test.
type osStack struct {
	s  *stack.Stack
	ep *channel.Endpoint
}

func newOSStack(t *testing.T) *osStack {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	ep := channel.New(256, 1400, "")
	if err := s.CreateNIC(1, ep); err != nil {
		t.Fatal(err)
	}
	pa := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(osAddr.As4()).WithPrefix(),
	}
	if err := s.AddProtocolAddress(1, pa, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	t.Cleanup(func() { ep.Close(); s.Close() })
	return &osStack{s: s, ep: ep}
}

// fakeTUN connects an osStack to the Stack under test.
type fakeTUN struct {
	os     *osStack
	ctx    context.Context
	closed chan struct{}
}

func (d *fakeTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	pkb := d.os.ep.ReadContext(d.ctx)
	if pkb == nil {
		return 0, errors.New("device closed")
	}
	defer pkb.DecRef()
	view := pkb.ToView()
	defer view.Release()
	sizes[0] = copy(bufs[0][offset:], view.AsSlice())
	return 1, nil
}

func (d *fakeTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		pkt := append([]byte(nil), b[offset:]...)
		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
		proto := header.IPv4ProtocolNumber
		if pkt[0]>>4 == 6 {
			proto = header.IPv6ProtocolNumber
		}
		d.os.ep.InjectInbound(proto, pkb)
		pkb.DecRef()
	}
	return len(bufs), nil
}

func (d *fakeTUN) BatchSize() int { return 1 }

// fakeUpstream emulates the phone: TCP connects to an in-process echo
// server, UDP is echoed back with a prefix, DNS answers are canned.
type fakeUpstream struct {
	stack *Stack
	mu    sync.Mutex
	dials []netip.AddrPort
}

type pipeConn struct{ net.Conn }

func (p pipeConn) CloseWrite() error { return p.Conn.(*net.TCPConn).CloseWrite() }
func (p pipeConn) Abort()            { p.Conn.Close() }

func (u *fakeUpstream) DialTCP(ctx context.Context, dst netip.AddrPort) (TCPConn, error) {
	u.mu.Lock()
	u.dials = append(u.dials, dst)
	u.mu.Unlock()
	if dst.Port() == 81 {
		return nil, proto.ResultError(proto.ResultConnectionRefused)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(c, c)
		_ = c.(*net.TCPConn).CloseWrite()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, err
	}
	return pipeConn{c}, nil
}

func (u *fakeUpstream) SendUDP(d proto.Datagram) error {
	reply := proto.Datagram{FlowID: d.FlowID, Addr: d.Addr, Payload: append([]byte("echo:"), d.Payload...)}
	go u.stack.HandleUDP(reply)
	return nil
}

func (u *fakeUpstream) ResolveRaw(_ context.Context, q []byte) ([]byte, error) {
	ans := append([]byte(nil), q...)
	ans[2] |= 0x80
	return ans, nil
}

func newTestStack(t *testing.T) (*osStack, *fakeUpstream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	os := newOSStack(t)
	dev := &fakeTUN{os: os, ctx: ctx}
	st, err := New(Config{Device: dev, MTU: 1400, DNS: []netip.Addr{dnsAddr}})
	if err != nil {
		t.Fatal(err)
	}
	up := &fakeUpstream{stack: st}
	st.SetUpstream(up)
	go st.Run(ctx)
	t.Cleanup(st.Close)
	return os, up
}

func fullAddr(ap netip.AddrPort) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFromSlice(ap.Addr().AsSlice()), Port: ap.Port()}
}

func TestTCPThroughStack(t *testing.T) {
	os, up := newTestStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dst := netip.MustParseAddrPort("93.184.216.34:443")
	c, err := gonet.DialContextTCP(ctx, os.s, fullAddr(dst), ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("dial through stack: %v", err)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("0123456789"), 50_000) // 500 KB
	go func() {
		_, _ = c.Write(payload)
		_ = c.CloseWrite()
	}()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %d bytes want %d", len(got), len(payload))
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.dials) != 1 || up.dials[0] != dst {
		t.Fatalf("upstream dials = %v, want [%s]", up.dials, dst)
	}
}

func TestTCPRefusedGetsReset(t *testing.T) {
	os, _ := newTestStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := gonet.DialContextTCP(ctx, os.s, fullAddr(netip.MustParseAddrPort("10.9.9.9:81")), ipv4.ProtocolNumber)
	if err == nil {
		t.Fatal("expected connection to be refused")
	}
	if ctx.Err() != nil {
		t.Fatal("connect timed out instead of being reset")
	}
}

func TestUDPThroughStack(t *testing.T) {
	os, _ := newTestStack(t)
	dst := netip.MustParseAddrPort("155.133.248.34:27017") // a Steam CM-style endpoint
	c, err := gonet.DialUDP(os.s, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFromSlice(dst.Addr().AsSlice()), Port: dst.Port()}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, size := range []int{10, 1300, 3000} { // 3000 exercises fragmented replies
		msg := bytes.Repeat([]byte{'x'}, size)
		if _, err := c.Write(msg); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 8192)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("size %d: read reply: %v", size, err)
		}
		if want := append([]byte("echo:"), msg...); !bytes.Equal(buf[:n], want) {
			t.Fatalf("size %d: reply mismatch (%d bytes)", size, n)
		}
	}
}

func TestDNSThroughStack(t *testing.T) {
	os, _ := newTestStack(t)
	c, err := gonet.DialUDP(os.s, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(dnsAddr.As4()), Port: 53}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	q := []byte{0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 5, 's', 't', 'e', 'a', 'm', 0, 0, 1, 0, 1}
	if _, err := c.Write(q); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(q) || buf[0] != 0xbe || buf[1] != 0xef || buf[2]&0x80 == 0 {
		t.Fatalf("unexpected DNS answer % x", buf[:n])
	}
}
