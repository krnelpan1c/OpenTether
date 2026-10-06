package relay

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Proxy-only mode serves devices that cannot run the desktop client
// (another phone, a console, a TV). They join the Wi-Fi Direct group and
// point their proxy settings at the phone. One port speaks both HTTP proxy
// (CONNECT and absolute-URI requests) and SOCKS5 (CONNECT and UDP
// ASSOCIATE), and serves a PAC file at /proxy.pac.
//
// Unlike the desktop client this only helps apps that honour proxy
// settings; it is the PdaNet-style fallback.

const proxyClientTTL = 2 * time.Minute

// ServeProxy accepts proxy connections on ln until ctx ends. Bind ln to the
// Wi-Fi Direct address only, so the proxy is reachable just by devices that
// know the group password.
func (s *Server) ServeProxy(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	s.log.Info("proxy listening", "addr", ln.Addr().String())
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go s.serveProxyConn(ctx, c)
	}
}

func (s *Server) serveProxyConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		s.mu.Lock()
		s.proxyClients[ap.Addr().Unmap()] = time.Now()
		s.mu.Unlock()
	}
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	conn := &bufferedConn{Conn: c, r: br}
	if first[0] == socksVersion {
		s.serveSOCKS(ctx, conn)
	} else {
		s.serveHTTPProxy(ctx, conn)
	}
}

func (s *Server) serveHTTPProxy(ctx context.Context, c *bufferedConn) {
	req, err := http.ReadRequest(c.r)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	switch {
	case req.Method == http.MethodConnect:
		remote, err := s.dialHost(ctx, withDefaultPort(req.Host, "443"))
		if err != nil {
			s.errs.Add(1)
			s.log.Debug("proxy CONNECT failed", "host", req.Host, "err", err)
			writeHTTPError(c, http.StatusBadGateway, err)
			return
		}
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			remote.Close()
			return
		}
		s.relayProxyTCP(c, remote)

	case req.URL.IsAbs():
		// Plain-HTTP proxy request: forward it in origin form. Each client
		// connection carries one request, so a keep-alive connection can't
		// end up talking to the wrong host.
		remote, err := s.dialHost(ctx, withDefaultPort(req.URL.Host, "80"))
		if err != nil {
			s.errs.Add(1)
			writeHTTPError(c, http.StatusBadGateway, err)
			return
		}
		for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Keep-Alive", "Connection"} {
			req.Header.Del(h)
		}
		req.Close = true
		req.RequestURI = ""
		if err := req.Write(&countingWriter{w: remote, n: &s.bytesUp}); err != nil {
			remote.Close()
			return
		}
		s.tcpTotal.Add(1)
		s.tcpActive.Add(1)
		defer s.tcpActive.Add(-1)
		_, _ = io.Copy(&countingWriter{w: c, n: &s.bytesDown}, remote)
		remote.Close()

	case req.URL.Path == "/proxy.pac" || req.URL.Path == "/wpad.dat":
		host := c.LocalAddr().String()
		pac := fmt.Sprintf(`function FindProxyForURL(url, host) {
  if (isPlainHostName(host) || shExpMatch(host, "192.168.49.*")) return "DIRECT";
  return "PROXY %s; SOCKS5 %s";
}
`, host, host)
		writeHTTP(c, http.StatusOK, "application/x-ns-proxy-autoconfig", pac)

	default:
		writeHTTP(c, http.StatusOK, "text/plain; charset=utf-8",
			"OpenTether proxy is running.\nSet this address as the HTTP or SOCKS5 proxy, or use /proxy.pac.\n")
	}
}

// relayProxyTCP pipes a proxy client connection to an upstream socket.
func (s *Server) relayProxyTCP(client *bufferedConn, remote *net.TCPConn) {
	s.tcpTotal.Add(1)
	s.tcpActive.Add(1)
	defer s.tcpActive.Add(-1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // device → internet
		defer wg.Done()
		if _, err := io.Copy(&countingWriter{w: remote, n: &s.bytesUp}, client); err != nil {
			_ = remote.SetLinger(0)
			_ = remote.Close()
			return
		}
		_ = remote.CloseWrite()
	}()
	go func() { // internet → device
		defer wg.Done()
		if _, err := io.Copy(&countingWriter{w: client, n: &s.bytesDown}, remote); err != nil {
			_ = client.Close()
			return
		}
		client.CloseWrite()
	}()
	wg.Wait()
	_ = remote.Close()
}

func withDefaultPort(host, port string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

func writeHTTP(w io.Writer, code int, contentType, body string) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), contentType, len(body), body)
}

func writeHTTPError(w io.Writer, code int, err error) {
	writeHTTP(w, code, "text/plain; charset=utf-8", "OpenTether: "+err.Error()+"\n")
}

// bufferedConn is a net.Conn whose reads drain a bufio.Reader first, so
// bytes read ahead while parsing the proxy request are not lost.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// CloseWrite half-closes the underlying TCP connection.
func (b *bufferedConn) CloseWrite() {
	if tc, ok := b.Conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}
