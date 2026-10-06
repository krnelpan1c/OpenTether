package netstack

import (
	"encoding/binary"
	"net/netip"
)

const (
	protoICMP   = 1
	protoTCP    = 6
	protoUDP    = 17
	protoFrag6  = 44
	protoICMPv6 = 58

	ipv4HeaderLen = 20
	ipv6HeaderLen = 40
	udpHeaderLen  = 8
	fragHeaderLen = 8
	defaultTTL    = 64
)

// ipPacket is the minimal view of an IP packet the fast path needs.
type ipPacket struct {
	version  int
	proto    uint8
	src, dst netip.Addr
	// fragment is set for IPv4 fragments and IPv6 packets whose first
	// extension header is a fragment header. Those go to the full stack
	// for reassembly.
	fragment bool
	// payload is the transport-layer bytes (header + data).
	payload []byte
}

// parseIP parses an IPv4 or IPv6 packet. ok is false for malformed input.
func parseIP(b []byte) (p ipPacket, ok bool) {
	if len(b) < 1 {
		return p, false
	}
	switch b[0] >> 4 {
	case 4:
		if len(b) < ipv4HeaderLen {
			return p, false
		}
		ihl := int(b[0]&0x0f) * 4
		total := int(binary.BigEndian.Uint16(b[2:4]))
		if ihl < ipv4HeaderLen || total < ihl || total > len(b) {
			return p, false
		}
		frag := binary.BigEndian.Uint16(b[6:8])
		p.version = 4
		p.fragment = frag&0x2000 != 0 || frag&0x1fff != 0
		p.proto = b[9]
		p.src = netip.AddrFrom4([4]byte(b[12:16]))
		p.dst = netip.AddrFrom4([4]byte(b[16:20]))
		p.payload = b[ihl:total]
		return p, true
	case 6:
		if len(b) < ipv6HeaderLen {
			return p, false
		}
		plen := int(binary.BigEndian.Uint16(b[4:6]))
		if ipv6HeaderLen+plen > len(b) {
			return p, false
		}
		p.version = 6
		p.proto = b[6]
		p.fragment = p.proto == protoFrag6
		p.src = netip.AddrFrom16([16]byte(b[8:24]))
		p.dst = netip.AddrFrom16([16]byte(b[24:40]))
		p.payload = b[ipv6HeaderLen : ipv6HeaderLen+plen]
		return p, true
	}
	return p, false
}

// parseUDP returns the ports and data of a UDP segment.
func parseUDP(seg []byte) (srcPort, dstPort uint16, data []byte, ok bool) {
	if len(seg) < udpHeaderLen {
		return 0, 0, nil, false
	}
	length := int(binary.BigEndian.Uint16(seg[4:6]))
	if length < udpHeaderLen || length > len(seg) {
		return 0, 0, nil, false
	}
	return binary.BigEndian.Uint16(seg[0:2]), binary.BigEndian.Uint16(seg[2:4]), seg[udpHeaderLen:length], true
}

// checksum folds a one's-complement sum over b into initial.
func checksum(b []byte, initial uint32) uint32 {
	sum := initial
	for len(b) >= 2 {
		sum += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return sum
}

func foldChecksum(sum uint32) uint16 {
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func pseudoHeaderSum(src, dst netip.Addr, proto uint8, length int) uint32 {
	sum := checksum(src.AsSlice(), 0)
	sum = checksum(dst.AsSlice(), sum)
	sum += uint32(proto)
	sum += uint32(length)
	return sum
}

// buildUDP builds the IP packet(s) carrying data from src to dst. Packets
// larger than mtu are fragmented. Both addresses must be the same family.
func buildUDP(src, dst netip.AddrPort, data []byte, mtu int, id uint32) [][]byte {
	udpLen := udpHeaderLen + len(data)
	if udpLen > 0xffff {
		return nil
	}
	seg := make([]byte, udpLen)
	binary.BigEndian.PutUint16(seg[0:2], src.Port())
	binary.BigEndian.PutUint16(seg[2:4], dst.Port())
	binary.BigEndian.PutUint16(seg[4:6], uint16(udpLen))
	copy(seg[udpHeaderLen:], data)
	sum := foldChecksum(checksum(seg, pseudoHeaderSum(src.Addr(), dst.Addr(), protoUDP, udpLen)))
	if sum == 0 {
		sum = 0xffff
	}
	binary.BigEndian.PutUint16(seg[6:8], sum)

	if src.Addr().Is4() {
		return buildIPv4(src.Addr(), dst.Addr(), protoUDP, seg, mtu, uint16(id))
	}
	return buildIPv6(src.Addr(), dst.Addr(), protoUDP, seg, mtu, id)
}

func buildIPv4(src, dst netip.Addr, proto uint8, l4 []byte, mtu int, id uint16) [][]byte {
	maxData := (mtu - ipv4HeaderLen) &^ 7
	if ipv4HeaderLen+len(l4) <= mtu {
		maxData = len(l4)
	}
	var out [][]byte
	for off := 0; off < len(l4); off += maxData {
		end := min(off+maxData, len(l4))
		pkt := make([]byte, ipv4HeaderLen+end-off)
		pkt[0] = 0x45
		binary.BigEndian.PutUint16(pkt[2:4], uint16(len(pkt)))
		binary.BigEndian.PutUint16(pkt[4:6], id)
		frag := uint16(off / 8)
		if end < len(l4) {
			frag |= 0x2000 // more fragments
		}
		binary.BigEndian.PutUint16(pkt[6:8], frag)
		pkt[8] = defaultTTL
		pkt[9] = proto
		s4, d4 := src.As4(), dst.As4()
		copy(pkt[12:16], s4[:])
		copy(pkt[16:20], d4[:])
		binary.BigEndian.PutUint16(pkt[10:12], foldChecksum(checksum(pkt[:ipv4HeaderLen], 0)))
		copy(pkt[ipv4HeaderLen:], l4[off:end])
		out = append(out, pkt)
	}
	return out
}

func buildIPv6(src, dst netip.Addr, proto uint8, l4 []byte, mtu int, id uint32) [][]byte {
	header := func(pkt []byte, next uint8) {
		pkt[0] = 0x60
		binary.BigEndian.PutUint16(pkt[4:6], uint16(len(pkt)-ipv6HeaderLen))
		pkt[6] = next
		pkt[7] = defaultTTL
		s16, d16 := src.As16(), dst.As16()
		copy(pkt[8:24], s16[:])
		copy(pkt[24:40], d16[:])
	}
	if ipv6HeaderLen+len(l4) <= mtu {
		pkt := make([]byte, ipv6HeaderLen+len(l4))
		header(pkt, proto)
		copy(pkt[ipv6HeaderLen:], l4)
		return [][]byte{pkt}
	}
	maxData := (mtu - ipv6HeaderLen - fragHeaderLen) &^ 7
	var out [][]byte
	for off := 0; off < len(l4); off += maxData {
		end := min(off+maxData, len(l4))
		pkt := make([]byte, ipv6HeaderLen+fragHeaderLen+end-off)
		header(pkt, protoFrag6)
		fh := pkt[ipv6HeaderLen:]
		fh[0] = proto
		offFlags := uint16(off/8) << 3
		if end < len(l4) {
			offFlags |= 1 // more fragments
		}
		binary.BigEndian.PutUint16(fh[2:4], offFlags)
		binary.BigEndian.PutUint32(fh[4:8], id)
		copy(pkt[ipv6HeaderLen+fragHeaderLen:], l4[off:end])
		out = append(out, pkt)
	}
	return out
}
