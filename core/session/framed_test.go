package session

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
)

// usbPipe imitates one direction of a USB bulk link: it records the size of
// every write (transfer) and delivers bytes to the reader.
type usbPipe struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	mu     sync.Mutex
	writes []int
}

func newUSBPipe() *usbPipe {
	pr, pw := io.Pipe()
	return &usbPipe{pr: pr, pw: pw}
}

func (u *usbPipe) Write(p []byte) (int, error) {
	u.mu.Lock()
	u.writes = append(u.writes, len(p))
	u.mu.Unlock()
	return u.pw.Write(p)
}

// link joins a write side and a read side into an io.ReadWriteCloser.
type link struct {
	in  *usbPipe // we read from here
	out *usbPipe // we write here
}

func (l link) Read(p []byte) (int, error)  { return l.in.pr.Read(p) }
func (l link) Write(p []byte) (int, error) { return l.out.Write(p) }
func (l link) Close() error                { l.out.pw.Close(); return l.in.pr.Close() }

func TestUSBStyleLink(t *testing.T) {
	aToB, bToA := newUSBPipe(), newUSBPipe()
	opts := LinkOptions{Name: "usb", TransferFraming: true, ReadSize: 16384, BatchWrites: true, PadMultiple: 512}
	a := NewFramedLink(link{in: bToA, out: aToB}, opts)
	b := NewFramedLink(link{in: aToB, out: bToA}, opts)
	defer a.Close()
	defer b.Close()

	// Sizes chosen so that frame+header lands exactly on 512-byte multiples.
	sizes := []int{510, 1022, 1452, 1, 1200, 510}
	var want [][]byte
	for i := 0; i < 300; i++ {
		p := bytes.Repeat([]byte{byte(i)}, sizes[i%len(sizes)])
		want = append(want, p)
	}
	go func() {
		for _, p := range want {
			if _, err := a.WriteTo(p, nil); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	buf := make([]byte, 2048)
	for i, w := range want {
		n, addr, err := b.ReadFrom(buf)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], w) {
			t.Fatalf("packet %d: got %d bytes, want %d", i, n, len(w))
		}
		if addr.String() != "peer:usb" {
			t.Fatalf("unexpected addr %s", addr)
		}
	}

	aToB.mu.Lock()
	defer aToB.mu.Unlock()
	if len(aToB.writes) >= len(want) {
		t.Errorf("expected batching: %d writes for %d packets", len(aToB.writes), len(want))
	}
	for _, n := range aToB.writes {
		if n%512 == 0 {
			t.Fatalf("write of %d bytes ends on a 512-byte boundary", n)
		}
	}
}

func TestFramedConnOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		pc := NewFramedPacketConn(c)
		buf := make([]byte, 2048)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], nil)
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	pc := NewFramedPacketConn(c)
	defer pc.Close()
	pc.WriteTo([]byte("ping"), nil)
	buf := make([]byte, 16)
	n, _, err := pc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("echo failed: %q %v", buf[:n], err)
	}
}

// A damaged transfer (cut short, or starting mid-frame) must only cost the
// packets inside it; every later transfer still parses.
func TestTransferFramingRecoversFromDamagedTransfers(t *testing.T) {
	pr, pw := io.Pipe()
	recv := NewFramedLink(link{in: &usbPipe{pr: pr, pw: pw}, out: newUSBPipe()}, LinkOptions{TransferFraming: true, ReadSize: 16384})
	defer recv.Close()

	frame := func(payload string) []byte {
		b := []byte{byte(len(payload) >> 8), byte(len(payload))}
		return append(b, payload...)
	}
	go func() {
		pw.Write(frame("first"))
		good := append(frame("lost-a"), frame("lost-b")...)
		pw.Write(good[:len(good)-3])                // truncated transfer
		pw.Write([]byte{0x7f, 0x01, 'j', 'u', 'n'}) // starts mid-frame: bogus length
		pw.Write(append(frame("second"), frame("third")...))
	}()

	buf := make([]byte, 64)
	var got []string
	for len(got) < 4 {
		n, _, err := recv.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(buf[:n]))
		if string(buf[:n]) == "third" {
			break
		}
	}
	// "lost-a" is intact inside the truncated transfer, so it is delivered;
	// "lost-b" and the junk are dropped.
	want := []string{"first", "lost-a", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if recv.Dropped() != 2 {
		t.Fatalf("dropped %d frames, want 2", recv.Dropped())
	}
}
