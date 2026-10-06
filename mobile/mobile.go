// Package mobile is the gomobile-facing API of the phone relay. Build it
// with `gomobile bind` (see scripts/build-android-core); the Android app
// talks to the relay exclusively through this package.
//
// Only types gomobile understands appear here: strings, []byte, bool,
// int32/int64, errors and interfaces.
package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/krnelpan1c/OpenTether/core/pairing"
	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/relay"
	"github.com/krnelpan1c/OpenTether/core/session"
)

// CoreVersion is reported in the app's about screen.
const CoreVersion = "0.1.0"

// Version returns the relay core version.
func Version() string { return CoreVersion }

// Platform is implemented by the Android app.
type Platform interface {
	// BindSocket binds the socket fd to the upstream network chosen in the
	// app (for example mobile data). It returns false if binding failed.
	BindSocket(fd int32) bool
	// ResolveRaw answers a raw DNS query using Android's resolver on the
	// upstream network. It returns nil or an empty array on failure.
	ResolveRaw(query []byte) []byte
	// HasIPv6 reports whether the upstream network has IPv6.
	HasIPv6() bool
	// Log forwards a log line; level follows log/slog (-4 debug, 0 info,
	// 4 warn, 8 error).
	Log(level int32, message string)
}

// Service owns the relay and its listeners.
type Service struct {
	platform Platform
	dataDir  string
	cert     tlsIdentity
	srv      *relay.Server

	mu          sync.Mutex
	token       string
	usbCtx      context.Context
	usbCancel   context.CancelFunc
	wifiCancel  context.CancelFunc
	proxyCancel context.CancelFunc
}

// NewService loads (or creates) the phone identity in dataDir and returns a
// stopped service.
func NewService(dataDir, deviceName string, p Platform) (*Service, error) {
	if p == nil {
		return nil, errors.New("platform is required")
	}
	cert, err := session.LoadOrCreateIdentity(filepath.Join(dataDir, "identity.pem"), "opentether-phone")
	if err != nil {
		return nil, err
	}
	s := &Service{platform: p, dataDir: dataDir, cert: tlsIdentity{cert}}
	if s.token, err = loadOrCreateToken(filepath.Join(dataDir, "token")); err != nil {
		return nil, err
	}
	s.srv = relay.New(relay.Config{
		Platform:   platformAdapter{p},
		Identity:   cert,
		ServerName: deviceName,
		Token:      s.Token,
		Logger:     newLogger(p),
	})
	return s, nil
}

// StartUSB listens on the abstract socket that `adb forward` connects to.
func (s *Service) StartUSB() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usbCancel != nil {
		return nil
	}
	ln, err := net.Listen("unix", "@"+proto.AbstractSocketName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.usbCtx, s.usbCancel = ctx, cancel
	go func() { _ = s.srv.ServeStream(ctx, ln, true) }()
	return nil
}

// StopUSB closes the USB listener and ends USB accessory sessions.
func (s *Service) StopUSB() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usbCancel != nil {
		s.usbCancel()
		s.usbCancel, s.usbCtx = nil, nil
	}
}

// ServeAccessory runs one session over an open USB accessory (Android Open
// Accessory) file descriptor and blocks until it ends. The service takes
// ownership of fd. USB mode must be on.
//
// The cable plus the user's consent to the system "open with" prompt
// authenticate the link, so no pairing token is needed.
func (s *Service) ServeAccessory(fd int32) error {
	s.mu.Lock()
	ctx := s.usbCtx
	s.mu.Unlock()
	f := os.NewFile(uintptr(fd), "usb-accessory")
	if ctx == nil {
		f.Close()
		return errors.New("USB sharing is off")
	}
	pc := session.NewFramedLink(f, session.LinkOptions{
		Name:            "usb-accessory",
		TransferFraming: true,
		ReadSize:        accessoryTransferSize,
		BatchWrites:     true,
		PadMultiple:     512,
	})
	return s.srv.ServeLink(ctx, pc, true, 0)
}

// accessoryTransferSize matches the Android accessory function's bulk
// buffer; reads must be this large or data is dropped.
const accessoryTransferSize = 16384

// StartWifi listens for QUIC on host:port, normally the Wi-Fi Direct group
// owner address (192.168.49.1). Clients must present the pairing token.
func (s *Service) StartWifi(host string, port int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wifiCancel != nil {
		return nil
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return err
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.wifiCancel = cancel
	go func() {
		_ = s.srv.ServePacket(ctx, pc, false)
		pc.Close()
	}()
	context.AfterFunc(ctx, func() { pc.Close() })
	return nil
}

// StopWifi closes the Wi-Fi listener and its sessions.
func (s *Service) StopWifi() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wifiCancel != nil {
		s.wifiCancel()
		s.wifiCancel = nil
	}
}

// StartProxy serves the HTTP/SOCKS5 proxy for devices without the desktop
// client on host:port. Bind it to the Wi-Fi Direct group owner address so
// only devices in the group can reach it.
func (s *Service) StartProxy(host string, port int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxyCancel != nil {
		return nil
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.proxyCancel = cancel
	go func() { _ = s.srv.ServeProxy(ctx, ln) }()
	return nil
}

// StopProxy closes the proxy listener.
func (s *Service) StopProxy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxyCancel != nil {
		s.proxyCancel()
		s.proxyCancel = nil
	}
}

// IsProxyRunning reports whether the proxy listener is active.
func (s *Service) IsProxyRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxyCancel != nil
}

// Stop closes every listener and session.
func (s *Service) Stop() {
	s.StopUSB()
	s.StopWifi()
	s.StopProxy()
	s.srv.CloseSessions()
}

// IsUSBRunning reports whether the USB listener is active.
func (s *Service) IsUSBRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usbCancel != nil
}

// IsWifiRunning reports whether the Wi-Fi listener is active.
func (s *Service) IsWifiRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wifiCancel != nil
}

// Fingerprint returns the phone's certificate fingerprint (hex).
func (s *Service) Fingerprint() string { return session.CertFingerprint(s.cert.Certificate) }

// ShortFingerprint returns a human-comparable fingerprint prefix.
func (s *Service) ShortFingerprint() string { return session.ShortFingerprint(s.Fingerprint()) }

// Token returns the current pairing token.
func (s *Service) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// RotateToken issues a new pairing token and disconnects every session so
// previously paired computers must pair again.
func (s *Service) RotateToken() (string, error) {
	tok := newToken()
	if err := os.WriteFile(filepath.Join(s.dataDir, "token"), []byte(tok), 0o600); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.token = tok
	s.mu.Unlock()
	s.srv.CloseSessions()
	return tok, nil
}

// PairingURI returns the opentether://pair link for a Wi-Fi Direct group.
func (s *Service) PairingURI(name, host string, port int32, ssid, passphrase string) string {
	return pairing.Pairing{
		Name:        name,
		Host:        host,
		Port:        int(port),
		Token:       s.Token(),
		Fingerprint: s.Fingerprint(),
		SSID:        ssid,
		Passphrase:  passphrase,
	}.URI()
}

// StatsJSON returns relay counters as JSON (see relay.Stats).
func (s *Service) StatsJSON() string {
	b, _ := json.Marshal(s.srv.Stats())
	return string(b)
}

func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tok := newToken()
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// newToken returns 80 random bits as "XXXX-XXXX-XXXX-XXXX" (base32).
func newToken() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// platformAdapter adapts the gomobile Platform to relay.Platform.
type platformAdapter struct{ p Platform }

func (a platformAdapter) BindSocket(fd int) error {
	if !a.p.BindSocket(int32(fd)) {
		return errors.New("could not bind socket to the upstream network")
	}
	return nil
}

func (a platformAdapter) ResolveRaw(ctx context.Context, q []byte) ([]byte, error) {
	type result struct{ b []byte }
	ch := make(chan result, 1)
	go func() { ch <- result{a.p.ResolveRaw(q)} }()
	select {
	case r := <-ch:
		if len(r.b) == 0 {
			return nil, errors.New("android resolver failed")
		}
		return r.b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a platformAdapter) HasIPv6() bool { return a.p.HasIPv6() }
