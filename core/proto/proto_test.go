package proto

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestAddrPortRoundTrip(t *testing.T) {
	for _, s := range []string{"1.2.3.4:80", "[2001:db8::1]:443", "[::ffff:10.0.0.1]:53"} {
		ap := netip.MustParseAddrPort(s)
		b := AppendAddrPort(nil, ap)
		got, n, err := ParseAddrPort(b)
		if err != nil || n != len(b) {
			t.Fatalf("%s: parse failed: n=%d err=%v", s, n, err)
		}
		want := netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		if got != want {
			t.Fatalf("%s: got %s want %s", s, got, want)
		}
		got2, err := ReadAddrPort(bytes.NewReader(b))
		if err != nil || got2 != want {
			t.Fatalf("%s: ReadAddrPort got %s, %v", s, got2, err)
		}
	}
}

func TestDatagramRoundTrip(t *testing.T) {
	d := Datagram{FlowID: 300, Addr: netip.MustParseAddrPort("[2001:db8::7]:27015"), Payload: []byte("steam")}
	got, err := ParseDatagram(AppendDatagram(nil, d))
	if err != nil {
		t.Fatal(err)
	}
	if got.FlowID != d.FlowID || got.Addr != d.Addr || !bytes.Equal(got.Payload, d.Payload) {
		t.Fatalf("got %+v want %+v", got, d)
	}
	if _, err := ParseDatagram([]byte{0x80}); err == nil {
		t.Fatal("expected error for truncated datagram")
	}
}

func TestFrameLimit(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFrame(&buf, 50); err == nil {
		t.Fatal("expected oversized frame to be rejected")
	}
}
