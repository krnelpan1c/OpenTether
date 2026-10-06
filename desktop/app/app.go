// Package app runs the desktop side of OpenTether: it brings up the virtual
// adapter, connects to the phone and keeps reconnecting while running.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/krnelpan1c/OpenTether/core/client"
	"github.com/krnelpan1c/OpenTether/core/netstack"
	"github.com/krnelpan1c/OpenTether/core/pairing"
	"github.com/krnelpan1c/OpenTether/core/proto"
	"github.com/krnelpan1c/OpenTether/core/session"
	"github.com/krnelpan1c/OpenTether/desktop/adb"
	"github.com/krnelpan1c/OpenTether/desktop/config"
	"github.com/krnelpan1c/OpenTether/desktop/osnet"
	"github.com/krnelpan1c/OpenTether/desktop/wifijoin"
)

// Link selects how the desktop reaches the phone.
type Link interface {
	// Key identifies the phone for certificate pinning.
	Key() string
	// Connect opens a packet link to the phone.
	Connect(ctx context.Context) (pc net.PacketConn, remote net.Addr, reliable bool, err error)
	// Token is the pairing token to present ("" for trusted links).
	Token() string
	// Pin is the expected fingerprint, if known from the link itself.
	Pin() string
	// Close releases link resources (such as adb forwards).
	Close()
}

// Status is reported whenever the connection state changes.
type Status struct {
	Connected bool
	Phone     string
	IPv6      bool
	Err       error
}

// Options configures Run.
type Options struct {
	Link       Link
	Store      *config.Store
	ClientName string
	Logger     *slog.Logger
	OnStatus   func(Status)
	// Stats, when set, is called every interval with combined counters.
	Stats         func(netstack.Stats)
	StatsInterval time.Duration
}

// Run creates the adapter and relays traffic until ctx ends.
func Run(ctx context.Context, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.OnStatus == nil {
		opts.OnStatus = func(Status) {}
	}
	defer opts.Link.Close()

	settings := osnet.Default
	adapter, err := osnet.Create(settings)
	if err != nil {
		return err
	}
	defer adapter.Close()
	log.Info("virtual adapter ready", "interface", adapter.InterfaceName(),
		"address", settings.Addr4, "dns", settings.DNS4)

	st, err := netstack.New(netstack.Config{
		Device: adapter,
		MTU:    settings.MTU,
		DNS:    []netip.Addr{settings.DNS4, settings.DNS6},
		Logger: log,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	runErr := make(chan error, 1)
	go func() { runErr <- st.Run(ctx) }()

	if opts.Stats != nil {
		interval := opts.StatsInterval
		if interval == 0 {
			interval = 5 * time.Second
		}
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					opts.Stats(st.Stats())
				}
			}
		}()
	}

	backoff := time.Second
	for ctx.Err() == nil {
		c, err := connect(ctx, opts, st, log)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			opts.OnStatus(Status{Err: err})
			if errors.Is(err, session.ErrFingerprintMismatch) {
				return err
			}
			select {
			case <-ctx.Done():
			case err := <-runErr:
				return fmt.Errorf("virtual adapter failed: %w", err)
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = time.Second
		st.SetUpstream(upstream{c})
		opts.OnStatus(Status{Connected: true, Phone: c.Info().ServerName, IPv6: c.Info().IPv6})

		select {
		case <-ctx.Done():
		case <-c.Done():
		case err := <-runErr:
			st.SetUpstream(nil)
			c.Close()
			return fmt.Errorf("virtual adapter failed: %w", err)
		}
		st.SetUpstream(nil)
		cause := c.Err()
		c.Close()
		if ctx.Err() == nil {
			opts.OnStatus(Status{Err: fmt.Errorf("connection to phone lost: %w", cause)})
		}
	}
	return nil
}

func connect(ctx context.Context, opts Options, st *netstack.Stack, log *slog.Logger) (*client.Client, error) {
	link := opts.Link
	pc, remote, reliable, err := link.Connect(ctx)
	if err != nil {
		return nil, err
	}
	// Links with an empty key (a USB accessory on the physical cable) are
	// not pinned: the cable itself is the trust anchor.
	key := link.Key()
	pin := link.Pin()
	if pin == "" && key != "" && opts.Store != nil {
		pin = opts.Store.Pin(key)
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := client.Dial(dctx, pc, remote, client.Options{
		Token:      link.Token(),
		ClientName: opts.ClientName,
		Pin:        pin,
		Reliable:   reliable,
		OnUDP:      st.HandleUDP,
		Logger:     log,
	})
	if err != nil {
		if h, ok := link.(dialHinter); ok {
			err = h.DialHint(err)
		}
		return nil, err
	}
	if pin == "" && key != "" && opts.Store != nil {
		if err := opts.Store.SetPin(key, c.Fingerprint()); err != nil {
			log.Warn("could not save phone fingerprint", "err", err)
		}
		log.Info("paired with phone", "fingerprint", session.ShortFingerprint(c.Fingerprint()))
	}
	return c, nil
}

// upstream adapts a client.Client to netstack.Upstream.
type upstream struct{ c *client.Client }

func (u upstream) DialTCP(ctx context.Context, dst netip.AddrPort) (netstack.TCPConn, error) {
	s, err := u.c.DialTCP(ctx, dst)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (u upstream) SendUDP(d proto.Datagram) error { return u.c.SendUDP(d) }

func (u upstream) ResolveRaw(ctx context.Context, q []byte) ([]byte, error) {
	return u.c.ResolveRaw(ctx, q)
}

// --- Links -------------------------------------------------------------------

// USBLink reaches the phone through adb forward.
type USBLink struct {
	ADB    adb.Client
	Serial string // optional

	device adb.Device
	port   int
}

func (l *USBLink) Key() string   { return "usb:" + l.device.Serial }
func (l *USBLink) Token() string { return "" }
func (l *USBLink) Pin() string   { return "" }

func (l *USBLink) Connect(ctx context.Context) (net.PacketConn, net.Addr, bool, error) {
	dev, err := l.ADB.PickDevice(ctx, l.Serial)
	if err != nil {
		return nil, nil, false, err
	}
	if dev.Serial != l.device.Serial || l.port == 0 {
		l.Close()
		port, err := l.ADB.Forward(ctx, dev.Serial, proto.AbstractSocketName)
		if err != nil {
			return nil, nil, false, err
		}
		l.device, l.port = dev, port
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(l.port)))
	if err != nil {
		l.port = 0
		return nil, nil, false, fmt.Errorf("adb forward is not accepting connections: %w", err)
	}
	pc := session.NewFramedPacketConn(conn)
	return pc, pc.RemoteAddr(), true, nil
}

func (l *USBLink) Close() {
	if l.port != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = l.ADB.RemoveForward(ctx, l.device.Serial, l.port)
		cancel()
		l.port = 0
	}
}

// StreamLink connects to a relay's stream listener directly. It is used
// with opentether-relay during development.
type StreamLink struct {
	Addr string
}

func (l *StreamLink) Key() string   { return "stream:" + l.Addr }
func (l *StreamLink) Token() string { return "" }
func (l *StreamLink) Pin() string   { return "" }
func (l *StreamLink) Close()        {}

func (l *StreamLink) Connect(ctx context.Context) (net.PacketConn, net.Addr, bool, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", l.Addr)
	if err != nil {
		return nil, nil, false, err
	}
	pc := session.NewFramedPacketConn(conn)
	return pc, pc.RemoteAddr(), true, nil
}

// WifiLink reaches the phone over Wi-Fi Direct using a pairing.
type WifiLink struct {
	Pairing pairing.Pairing
	// AutoJoin connects the computer to the phone's Wi-Fi Direct network
	// when it isn't on it already, and reconnects the previous network on
	// Close.
	AutoJoin bool
	Log      *slog.Logger

	lastJoin time.Time
	restore  func()
}

func (l *WifiLink) Key() string   { return "wifi:" + l.Pairing.Fingerprint }
func (l *WifiLink) Token() string { return l.Pairing.Token }
func (l *WifiLink) Pin() string   { return l.Pairing.Fingerprint }

func (l *WifiLink) Close() {
	if l.restore != nil {
		l.restore()
		l.restore = nil
	}
}

func (l *WifiLink) Connect(ctx context.Context) (net.PacketConn, net.Addr, bool, error) {
	raddr, err := net.ResolveUDPAddr("udp", l.Pairing.Addr())
	if err != nil {
		return nil, nil, false, err
	}
	if err := l.ensureJoined(ctx, raddr); err != nil {
		return nil, nil, false, err
	}
	pc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, nil, false, err
	}
	return pc, raddr, false, nil
}

// ensureJoined joins the phone's network when the computer has no address
// in the phone's /24 yet. Attempts are spaced out so a failing join doesn't
// thrash the Wi-Fi adapter.
func (l *WifiLink) ensureJoined(ctx context.Context, raddr *net.UDPAddr) error {
	if !l.AutoJoin || l.Pairing.SSID == "" {
		return nil
	}
	host, ok := netip.AddrFromSlice(raddr.IP)
	if !ok {
		return nil
	}
	prefix := netip.PrefixFrom(host.Unmap(), 24).Masked()
	if wifijoin.Joined(prefix) || time.Since(l.lastJoin) < 20*time.Second {
		return nil
	}
	l.lastJoin = time.Now()
	if l.Log != nil {
		l.Log.Info("joining the phone's Wi-Fi Direct network", "ssid", l.Pairing.SSID)
	}
	restore, err := wifijoin.Join(ctx, l.Pairing.SSID, l.Pairing.Passphrase, prefix)
	if l.restore == nil {
		l.restore = restore // remember the network we started on
	}
	if err != nil {
		return fmt.Errorf("could not join %q automatically (join it manually): %w", l.Pairing.SSID, err)
	}
	return nil
}

// Hostname returns a friendly name for this computer.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "computer"
	}
	return h
}
