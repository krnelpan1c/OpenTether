package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/krnelpan1c/OpenTether/core/session"
)

// fakeDNS answers every A query with 127.0.0.1 and returns no AAAA records.
func fakeDNS(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) == 0 {
				continue
			}
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true},
				Questions: q.Questions,
			}
			if q.Questions[0].Type == dnsmessage.TypeA {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
				}}
			}
			b, _ := resp.Pack()
			_, _ = pc.WriteTo(b, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func startProxy(t *testing.T) (*Server, string) {
	t.Helper()
	pemData, err := session.GenerateIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := session.ParseIdentity(pemData)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Identity:    cert,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		FallbackDNS: fakeDNS(t),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.ServeProxy(ctx, ln)
	return srv, ln.Addr().String()
}

func tcpEcho(t *testing.T) uint16 {
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
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String()).Port()
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("echo got %q want %q", buf, msg)
	}
}

func TestHTTPConnect(t *testing.T) {
	srv, proxyAddr := startProxy(t)
	port := tcpEcho(t)
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A hostname exercises the lookup through the phone's resolver.
	target := net.JoinHostPort("echo.example", strconv.Itoa(int(port)))
	io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v %v", resp, err)
	}
	roundTrip(t, &bufConn{Conn: c, r: br}, "hello through CONNECT")
	if len(srv.Stats().Clients) != 1 || !strings.HasSuffix(srv.Stats().Clients[0], "(proxy)") {
		t.Fatalf("proxy client not listed: %v", srv.Stats().Clients)
	}
}

func TestHTTPAbsoluteRequest(t *testing.T) {
	_, proxyAddr := startProxy(t)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Connection") != "" {
			t.Error("hop-by-hop header leaked")
		}
		io.WriteString(w, "page for "+r.URL.Path)
	}))
	defer web.Close()
	port := netip.MustParseAddrPort(strings.TrimPrefix(web.URL, "http://")).Port()

	proxyURL, _ := http.NewRequest("GET", "http://"+proxyAddr, nil)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL.URL)}, Timeout: 5 * time.Second}
	resp, err := client.Get("http://site.example:" + strconv.Itoa(int(port)) + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "page for /hello" {
		t.Fatalf("unexpected body %q", body)
	}
}

func TestPAC(t *testing.T) {
	_, proxyAddr := startProxy(t)
	resp, err := http.Get("http://" + proxyAddr + "/proxy.pac")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "PROXY "+proxyAddr) || !strings.Contains(resp.Header.Get("Content-Type"), "proxy-autoconfig") {
		t.Fatalf("bad PAC: %s", body)
	}
}

func socksHandshake(t *testing.T, c net.Conn, cmd byte, host string, port uint16) []byte {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil || greet != [2]byte{5, 0} {
		t.Fatalf("greeting failed: %v %v", greet, err)
	}
	req := []byte{5, cmd, 0, socksAtypDomain, byte(len(host))}
	req = append(req, host...)
	req = binary.BigEndian.AppendUint16(req, port)
	c.Write(req)
	reply := make([]byte, 10) // IPv4 bound address
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != socksSucceeded {
		t.Fatalf("socks reply %d", reply[1])
	}
	return reply
}

func TestSOCKSConnect(t *testing.T) {
	_, proxyAddr := startProxy(t)
	port := tcpEcho(t)
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	socksHandshake(t, c, socksCmdConnect, "echo.example", port)
	roundTrip(t, c, "hello through SOCKS5")
}

func TestSOCKSUDPAssociate(t *testing.T) {
	_, proxyAddr := startProxy(t)
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			echo.WriteTo(buf[:n], a)
		}
	}()
	echoAddr := netip.MustParseAddrPort(echo.LocalAddr().String())

	ctrl, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	reply := socksHandshake(t, ctrl, socksCmdUDP, "0.0.0.0", 0)
	relayAddr := netip.AddrPortFrom(netip.AddrFrom4([4]byte(reply[4:8])), binary.BigEndian.Uint16(reply[8:10]))

	uc, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(relayAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	pkt := appendSocksUDPHeader(nil, echoAddr)
	pkt = append(pkt, "game packet"...)
	uc.Write(pkt)
	_ = uc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2048)
	n, err := uc.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	host, port, payload, err := parseSocksUDP(buf[:n])
	if err != nil || host != echoAddr.Addr().String() || port != echoAddr.Port() || !bytes.Equal(payload, []byte("game packet")) {
		t.Fatalf("bad UDP reply host=%s port=%d payload=%q err=%v", host, port, payload, err)
	}
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
