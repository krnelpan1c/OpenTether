package relay

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// lookupHost resolves host through the upstream resolver (the platform's,
// so the lookup follows the selected network and Private DNS). IPv4
// addresses come first; IPv6 ones are included when the upstream has IPv6.
func (s *Server) lookupHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	if !strings.HasSuffix(host, ".") {
		host += "."
	}
	name, err := dnsmessage.NewName(host)
	if err != nil {
		return nil, fmt.Errorf("invalid host name %q", host)
	}
	types := []dnsmessage.Type{dnsmessage.TypeA}
	if s.hasIPv6() {
		types = append(types, dnsmessage.TypeAAAA)
	}
	results := make([][]netip.Addr, len(types))
	errs := make([]error, len(types))
	done := make(chan int, len(types))
	for i, t := range types {
		go func() {
			results[i], errs[i] = s.query(ctx, name, t)
			done <- i
		}()
	}
	for range types {
		<-done
	}
	var addrs []netip.Addr
	for _, r := range results {
		addrs = append(addrs, r...)
	}
	if len(addrs) == 0 {
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no addresses for %s", strings.TrimSuffix(host, "."))
	}
	return addrs, nil
}

func (s *Server) query(ctx context.Context, name dnsmessage.Name, t dnsmessage.Type) ([]netip.Addr, error) {
	var id [2]byte
	_, _ = rand.Read(id[:])
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: binary.BigEndian.Uint16(id[:]), RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: t, Class: dnsmessage.ClassINET}},
	}
	q, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.dnsQueries.Add(1)
	ans, err := s.resolve(qctx, q)
	if err != nil {
		return nil, err
	}

	var p dnsmessage.Parser
	h, err := p.Start(ans)
	if err != nil {
		return nil, err
	}
	if h.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("lookup %s: %s", name, h.RCode)
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	for {
		rh, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, netip.AddrFrom4(r.A))
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, netip.AddrFrom16(r.AAAA))
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil, err
			}
		}
	}
	return addrs, nil
}

// dialHost connects to host:port through the upstream network, trying each
// resolved address in turn.
func (s *Server) dialHost(ctx context.Context, hostport string) (*net.TCPConn, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	addrs, err := s.lookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, a := range addrs {
		if a.Is6() && !s.hasIPv6() {
			continue
		}
		actx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c, err := s.dialer().DialContext(actx, "tcp", netip.AddrPortFrom(a, uint16(port)).String())
		cancel()
		if err == nil {
			return c.(*net.TCPConn), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no reachable address for %s", host)
	}
	return nil, lastErr
}
