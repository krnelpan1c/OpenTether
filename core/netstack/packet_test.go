package netstack

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
)

func verifyUDPChecksum(t *testing.T, p ipPacket) {
	t.Helper()
	sum := checksum(p.payload, pseudoHeaderSum(p.src, p.dst, protoUDP, len(p.payload)))
	if foldChecksum(sum) != 0 {
		t.Fatalf("bad UDP checksum")
	}
}

func TestBuildParseUDP(t *testing.T) {
	for _, tc := range []struct{ src, dst string }{
		{"8.8.8.8:53", "172.19.0.1:50000"},
		{"[2001:4860:4860::8888]:53", "[fdfe:dcba:9876::1]:50000"},
	} {
		src, dst := netip.MustParseAddrPort(tc.src), netip.MustParseAddrPort(tc.dst)
		data := []byte("hello over the tunnel")
		pkts := buildUDP(src, dst, data, 1400, 7)
		if len(pkts) != 1 {
			t.Fatalf("%s: expected 1 packet, got %d", tc.src, len(pkts))
		}
		p, ok := parseIP(pkts[0])
		if !ok || p.fragment || p.proto != protoUDP || p.src != src.Addr() || p.dst != dst.Addr() {
			t.Fatalf("%s: parse mismatch: %+v ok=%v", tc.src, p, ok)
		}
		if p.version == 4 && foldChecksum(checksum(pkts[0][:ipv4HeaderLen], 0)) != 0 {
			t.Fatal("bad IPv4 header checksum")
		}
		verifyUDPChecksum(t, p)
		sp, dp, got, ok := parseUDP(p.payload)
		if !ok || sp != src.Port() || dp != dst.Port() || !bytes.Equal(got, data) {
			t.Fatalf("%s: udp mismatch", tc.src)
		}
	}
}

func TestBuildUDPFragmentsIPv4(t *testing.T) {
	src, dst := netip.MustParseAddrPort("1.1.1.1:53"), netip.MustParseAddrPort("172.19.0.1:1234")
	data := bytes.Repeat([]byte{0xAB}, 4000)
	pkts := buildUDP(src, dst, data, 1400, 99)
	if len(pkts) < 3 {
		t.Fatalf("expected fragmentation, got %d packets", len(pkts))
	}
	var l4 []byte
	for i, pkt := range pkts {
		if len(pkt) > 1400 {
			t.Fatalf("fragment %d exceeds MTU: %d", i, len(pkt))
		}
		p, ok := parseIP(pkt)
		if !ok || !p.fragment {
			t.Fatalf("fragment %d not recognised", i)
		}
		frag := binary.BigEndian.Uint16(pkt[6:8])
		if off := int(frag&0x1fff) * 8; off != len(l4) {
			t.Fatalf("fragment %d offset %d, want %d", i, off, len(l4))
		}
		more := frag&0x2000 != 0
		if more != (i < len(pkts)-1) {
			t.Fatalf("fragment %d MF flag wrong", i)
		}
		l4 = append(l4, p.payload...)
	}
	_, _, got, ok := parseUDP(l4)
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("reassembled payload mismatch")
	}
}

func TestBuildUDPFragmentsIPv6(t *testing.T) {
	src, dst := netip.MustParseAddrPort("[2001:db8::1]:53"), netip.MustParseAddrPort("[fdfe:dcba:9876::1]:1234")
	data := bytes.Repeat([]byte{0xCD}, 3000)
	pkts := buildUDP(src, dst, data, 1400, 42)
	var l4 []byte
	for i, pkt := range pkts {
		if len(pkt) > 1400 {
			t.Fatalf("fragment %d exceeds MTU", i)
		}
		p, ok := parseIP(pkt)
		if !ok || !p.fragment {
			t.Fatalf("fragment %d not recognised", i)
		}
		fh := p.payload[:fragHeaderLen]
		if fh[0] != protoUDP || binary.BigEndian.Uint32(fh[4:8]) != 42 {
			t.Fatalf("fragment %d header wrong", i)
		}
		l4 = append(l4, p.payload[fragHeaderLen:]...)
	}
	_, _, got, ok := parseUDP(l4)
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("reassembled payload mismatch")
	}
}

func TestServfail(t *testing.T) {
	q := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 3, 'f', 'o', 'o', 0, 0, 1, 0, 1}
	r := servfail(q)
	if r[0] != 0x12 || r[1] != 0x34 || r[2]&0x80 == 0 || r[3]&0x0f != 2 {
		t.Fatalf("bad servfail header: % x", r[:4])
	}
}
