package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/krnelpan1c/OpenTether/core/client"
	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/relay"
	"github.com/krnelpan1c/OpenTether/core/session"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newRelay(t *testing.T, dns string) (*relay.Server, string) {
	t.Helper()
	pemData, err := session.GenerateIdentity("test-phone")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := session.ParseIdentity(pemData)
	if err != nil {
		t.Fatal(err)
	}
	srv := relay.New(relay.Config{
		Identity:    cert,
		ServerName:  "Test Phone",
		Token:       func() string { return "secret-token" },
		Logger:      quietLogger(),
		FallbackDNS: dns,
	})
	return srv, session.CertFingerprint(cert)
}

// startStreamRelay serves the relay over a TCP listener, like adb forward.
func startStreamRelay(t *testing.T, srv *relay.Server) string {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServeStream(ctx, ln, true)
	return ln.Addr().String()
}

func dialStream(t *testing.T, addr string, opts client.Options) *client.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	pc := session.NewFramedPacketConn(conn)
	opts.Reliable = true
	opts.Logger = quietLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, pc, pc.RemoteAddr(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func startTCPEcho(t *testing.T) netip.AddrPort {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.(*net.TCPConn).CloseWrite()
			}()
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func startUDPEcho(t *testing.T) (netip.AddrPort, *net.UDPConn) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 0xFFFF)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return netip.MustParseAddrPort(pc.LocalAddr().String()), pc
}

func TestStreamSessionTCP(t *testing.T) {
	srv, fp := newRelay(t, "")
	addr := startStreamRelay(t, srv)
	c := dialStream(t, addr, client.Options{ClientName: "test-pc"})
	if c.Fingerprint() != fp {
		t.Fatalf("fingerprint %s, want %s", c.Fingerprint(), fp)
	}
	if !c.Info().OK || c.Info().ServerName != "Test Phone" {
		t.Fatalf("unexpected hello ack %+v", c.Info())
	}

	echo := startTCPEcho(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := c.DialTCP(ctx, echo)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("opentether"), 200_000) // 2 MB
	go func() {
		_, _ = st.Write(payload)
		_ = st.CloseWrite()
	}()
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: %d bytes, want %d", len(got), len(payload))
	}
	_ = st.Close()

	// A closed port must be reported as refused.
	closed := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), freePort(t))
	_, err = c.DialTCP(ctx, closed)
	var re proto.ResultError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResultError, got %v", err)
	}

	waitFor(t, func() bool { s := srv.Stats(); return s.BytesUp >= int64(len(payload)) && s.TCPActive == 0 })
	if s := srv.Stats(); s.Sessions != 1 || s.Clients[0] != "test-pc" {
		t.Fatalf("unexpected stats %+v", s)
	}
}

func TestStreamSessionUDPAndOverflow(t *testing.T) {
	srv, _ := newRelay(t, "")
	addr := startStreamRelay(t, srv)
	got := make(chan proto.Datagram, 4)
	c := dialStream(t, addr, client.Options{OnUDP: func(d proto.Datagram) {
		d.Payload = append([]byte(nil), d.Payload...)
		got <- d
	}})
	echo, _ := startUDPEcho(t)
	for _, size := range []int{32, 1300, 9000} { // 9000 must take the overflow stream
		msg := bytes.Repeat([]byte{byte(size)}, size)
		if err := c.SendUDP(proto.Datagram{FlowID: 7, Addr: echo, Payload: msg}); err != nil {
			t.Fatal(err)
		}
		select {
		case d := <-got:
			if d.FlowID != 7 || d.Addr != echo || !bytes.Equal(d.Payload, msg) {
				t.Fatalf("size %d: bad reply flow=%d addr=%s len=%d", size, d.FlowID, d.Addr, len(d.Payload))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("size %d: no UDP reply", size)
		}
	}
	if s := srv.Stats(); s.UDPFlows != 1 {
		t.Fatalf("expected one UDP flow, got %d", s.UDPFlows)
	}
}

func TestDNS(t *testing.T) {
	dnsAddr, _ := startUDPEcho(t) // echoes the query back as the "answer"
	srv, _ := newRelay(t, dnsAddr.String())
	addr := startStreamRelay(t, srv)
	c := dialStream(t, addr, client.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := []byte{1, 2, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	ans, err := c.ResolveRaw(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ans, q) {
		t.Fatalf("unexpected answer % x", ans)
	}
}

func TestWifiRequiresTokenAndPin(t *testing.T) {
	srv, fp := newRelay(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.ServePacket(ctx, pc, false)

	dial := func(opts client.Options) (*client.Client, error) {
		cpc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
		defer dcancel()
		opts.Logger = quietLogger()
		return client.Dial(dctx, cpc, pc.LocalAddr(), opts)
	}

	if _, err := dial(client.Options{Token: "wrong", Pin: fp}); err == nil {
		t.Fatal("wrong token was accepted")
	}
	if _, err := dial(client.Options{Token: "secret-token", Pin: "00" + fp[2:]}); !errors.Is(err, session.ErrFingerprintMismatch) {
		t.Fatalf("expected fingerprint mismatch, got %v", err)
	}
	c, err := dial(client.Options{Token: "secret-token", Pin: fp})
	if err != nil {
		t.Fatalf("valid pairing rejected: %v", err)
	}
	c.Close()
}

func freePort(t *testing.T) uint16 {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := netip.MustParseAddrPort(ln.Addr().String()).Port()
	ln.Close()
	return port
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// silentLink is a byte link without read deadlines whose peer never
// answers, like a USB accessory before the phone app opens it.
type silentLink struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (s silentLink) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s silentLink) Write(p []byte) (int, error) { return len(p), nil }
func (s silentLink) Close() error                { s.w.Close(); return s.r.Close() }

// A failed handshake must return promptly even when the link ignores read
// deadlines (it used to hang in quic.Transport.Close).
func TestDialFailsPromptlyOnDeadlinelessLink(t *testing.T) {
	r, w := io.Pipe()
	pc := session.NewFramedLink(silentLink{r: r, w: w}, session.LinkOptions{Name: "silent"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Dial(ctx, pc, pc.RemoteAddr(), client.Options{Reliable: true, Logger: quietLogger()})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handshake with a silent peer succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Dial hung after the handshake timed out")
	}
}
