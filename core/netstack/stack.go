// Package netstack terminates the desktop's IP traffic in userspace. Raw IP
// packets read from a TUN device are turned into upstream requests:
//
//   - TCP is terminated by gVisor's netstack and each connection becomes a
//     proxied stream on the phone.
//   - UDP is handled on a fast path without the full stack: each local source
//     endpoint becomes one flow on the phone, and replies are written back as
//     hand-built IP packets so any remote address can answer.
//   - DNS sent to the virtual resolver address is answered by the phone's
//     resolver.
//   - Fragmented UDP goes through gVisor for reassembly first.
//   - Everything else (ICMP, multicast, ...) is dropped.
package netstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

// DeviceOffset is the headroom reserved in front of every packet buffer
// passed to Device.Read and Device.Write (needed by Linux virtio headers).
const DeviceOffset = 16

const nicID tcpip.NICID = 1

// Device is the subset of wireguard-go's tun.Device used by the stack.
type Device interface {
	Read(bufs [][]byte, sizes []int, offset int) (int, error)
	Write(bufs [][]byte, offset int) (int, error)
	BatchSize() int
}

// TCPConn is a proxied TCP connection provided by an Upstream.
type TCPConn interface {
	io.ReadWriter
	// CloseWrite sends FIN to the remote host.
	CloseWrite() error
	// Abort resets the connection.
	Abort()
	// Close releases the connection once both directions are done.
	Close() error
}

// Upstream carries traffic to the phone.
type Upstream interface {
	DialTCP(ctx context.Context, dst netip.AddrPort) (TCPConn, error)
	SendUDP(d proto.Datagram) error
	ResolveRaw(ctx context.Context, query []byte) ([]byte, error)
}

// Config configures a Stack.
type Config struct {
	Device Device
	MTU    int
	// DNS lists the virtual resolver addresses answered by the phone.
	DNS    []netip.Addr
	Logger *slog.Logger
}

// Stats is a snapshot of desktop-side counters.
type Stats struct {
	TCPActive  int64 `json:"tcpActive"`
	UDPFlows   int64 `json:"udpFlows"`
	DNSQueries int64 `json:"dnsQueries"`
	Dropped    int64 `json:"dropped"`
	BytesUp    int64 `json:"bytesUp"`
	BytesDown  int64 `json:"bytesDown"`
}

// Stack bridges a TUN device to an Upstream.
type Stack struct {
	cfg    Config
	log    *slog.Logger
	ns     *stack.Stack
	ep     *channel.Endpoint
	ctx    context.Context
	cancel context.CancelFunc

	up atomic.Pointer[upstreamBox]

	writeMu sync.Mutex
	ipID    atomic.Uint32

	udpMu     sync.Mutex
	flowBySrc map[netip.AddrPort]*udpFlow
	flowByID  map[uint64]*udpFlow
	nextFlow  uint64

	tcpActive, dnsQueries, dropped atomic.Int64
	bytesUp, bytesDown             atomic.Int64
}

type upstreamBox struct{ u Upstream }

type udpFlow struct {
	id       uint64
	src      netip.AddrPort
	lastUsed atomic.Int64
}

func (f *udpFlow) touch() { f.lastUsed.Store(time.Now().UnixNano()) }

// New creates a Stack. Call Run to start moving packets.
func New(cfg Config) (*Stack, error) {
	if cfg.MTU == 0 {
		cfg.MTU = 1400
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Stack{
		cfg:       cfg,
		log:       cfg.Logger,
		flowBySrc: make(map[netip.AddrPort]*udpFlow),
		flowByID:  make(map[uint64]*udpFlow),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.ns = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	s.ep = channel.New(1024, uint32(cfg.MTU), "")
	if err := s.ns.CreateNIC(nicID, s.ep); err != nil {
		return nil, fmt.Errorf("netstack: create NIC: %v", err)
	}
	// Accept packets for every address and let us answer as any address.
	if err := s.ns.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("netstack: promiscuous mode: %v", err)
	}
	if err := s.ns.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("netstack: spoofing: %v", err)
	}
	s.ns.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	sack := tcpip.TCPSACKEnabled(true)
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	moderate := tcpip.TCPModerateReceiveBufferOption(true)
	for _, opt := range []tcpip.SettableTransportProtocolOption{&sack, &rcv, &snd, &moderate} {
		if err := s.ns.SetTransportProtocolOption(tcp.ProtocolNumber, opt); err != nil {
			return nil, fmt.Errorf("netstack: tcp option %T: %v", opt, err)
		}
	}

	tcpFwd := tcp.NewForwarder(s.ns, 0, 4096, s.handleTCP)
	s.ns.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s.ns, s.handleReassembledUDP)
	s.ns.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return s, nil
}

// SetUpstream switches traffic to u. Passing nil makes new connections fail
// fast until an upstream is available again.
func (s *Stack) SetUpstream(u Upstream) {
	if u == nil {
		s.up.Store(nil)
		return
	}
	s.up.Store(&upstreamBox{u: u})
}

func (s *Stack) upstream() Upstream {
	if b := s.up.Load(); b != nil {
		return b.u
	}
	return nil
}

// Stats returns a snapshot of the stack counters.
func (s *Stack) Stats() Stats {
	s.udpMu.Lock()
	flows := len(s.flowByID)
	s.udpMu.Unlock()
	return Stats{
		TCPActive:  s.tcpActive.Load(),
		UDPFlows:   int64(flows),
		DNSQueries: s.dnsQueries.Load(),
		Dropped:    s.dropped.Load(),
		BytesUp:    s.bytesUp.Load(),
		BytesDown:  s.bytesDown.Load(),
	}
}

// Run moves packets until ctx ends or the device fails. Closing the device
// is the caller's job and is what unblocks a pending device read.
func (s *Stack) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	go s.writeLoop(ctx)
	go s.reapLoop(ctx)
	err := s.readLoop(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Close stops the stack.
func (s *Stack) Close() {
	s.cancel()
	s.ep.Close()
	s.ns.Close()
}

func (s *Stack) readLoop(ctx context.Context) error {
	batch := max(s.cfg.Device.BatchSize(), 1)
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, DeviceOffset+0xFFFF)
	}
	sizes := make([]int, batch)
	for ctx.Err() == nil {
		n, err := s.cfg.Device.Read(bufs, sizes, DeviceOffset)
		for i := 0; i < n; i++ {
			s.handleInbound(bufs[i][DeviceOffset : DeviceOffset+sizes[i]])
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// handleInbound dispatches one packet the desktop OS sent into the tunnel.
func (s *Stack) handleInbound(b []byte) {
	p, ok := parseIP(b)
	if !ok {
		s.dropped.Add(1)
		return
	}
	switch {
	case p.proto == protoUDP && !p.fragment:
		srcPort, dstPort, data, ok := parseUDP(p.payload)
		if !ok {
			s.dropped.Add(1)
			return
		}
		s.routeUDP(netip.AddrPortFrom(p.src, srcPort), netip.AddrPortFrom(p.dst, dstPort), data)
	case p.proto == protoTCP || p.fragment:
		s.inject(p.version, b)
	default:
		// ICMP and friends: answering them locally would fake reachability
		// and latency, so drop them.
		s.dropped.Add(1)
	}
}

func (s *Stack) inject(version int, b []byte) {
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(append([]byte(nil), b...)),
	})
	if version == 4 {
		s.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
	} else {
		s.ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
	}
	pkb.DecRef()
}

// writeLoop copies packets produced by gVisor to the device.
func (s *Stack) writeLoop(ctx context.Context) {
	for {
		pkb := s.ep.ReadContext(ctx)
		if pkb == nil {
			return
		}
		view := pkb.ToView()
		s.writePackets([][]byte{view.AsSlice()})
		view.Release()
		pkb.DecRef()
	}
}

func (s *Stack) writePackets(pkts [][]byte) {
	if len(pkts) == 0 {
		return
	}
	bufs := make([][]byte, len(pkts))
	for i, p := range pkts {
		buf := make([]byte, DeviceOffset+len(p))
		copy(buf[DeviceOffset:], p)
		bufs[i] = buf
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.cfg.Device.Write(bufs, DeviceOffset); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Debug("tun write failed", "err", err)
	}
}

func (s *Stack) isDNS(a netip.Addr) bool {
	for _, d := range s.cfg.DNS {
		if d == a {
			return true
		}
	}
	return false
}

func toAddr(a tcpip.Address) netip.Addr {
	addr, _ := netip.AddrFromSlice(a.AsSlice())
	return addr.Unmap()
}
