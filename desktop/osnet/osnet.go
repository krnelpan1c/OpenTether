// Package osnet creates the OpenTether virtual adapter and points the
// operating system's routes and DNS at it.
package osnet

import (
	"net/netip"

	"golang.zx2c4.com/wireguard/tun"
)

// Settings describes the virtual adapter.
type Settings struct {
	// Name is the requested interface name (ignored on macOS, which
	// always allocates utunN).
	Name string
	MTU  int
	// Addr4/Addr6 are the computer's addresses on the tunnel.
	Addr4, Addr6 netip.Prefix
	// DNS4/DNS6 are the virtual resolvers answered through the phone.
	DNS4, DNS6 netip.Addr
}

// Default is the standard adapter layout. 172.19.0.0/30 and a ULA /126 keep
// clear of common home, carrier-grade NAT and Wi-Fi Direct ranges.
var Default = Settings{
	Name:  "OpenTether",
	MTU:   1400,
	Addr4: netip.MustParsePrefix("172.19.0.1/30"),
	DNS4:  netip.MustParseAddr("172.19.0.2"),
	Addr6: netip.MustParsePrefix("fdfe:dcba:9876::1/126"),
	DNS6:  netip.MustParseAddr("fdfe:dcba:9876::2"),
}

// The default route is split in halves so it wins over the existing default
// route without deleting it, while more specific routes (like the Wi-Fi
// Direct subnet the phone is reached through) keep working.
var (
	routes4 = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
	routes6 = []netip.Prefix{netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1")}
)

// Adapter is a configured virtual adapter.
type Adapter struct {
	tun.Device
	name    string
	cleanup func()
}

// InterfaceName returns the OS name of the adapter.
func (a *Adapter) InterfaceName() string { return a.name }

// Close restores the OS configuration and removes the adapter.
func (a *Adapter) Close() error {
	if a.cleanup != nil {
		a.cleanup()
	}
	return a.Device.Close()
}

// Create creates the adapter and routes all traffic through it.
func Create(s Settings) (*Adapter, error) {
	dev, err := createTUN(s)
	if err != nil {
		return nil, err
	}
	name, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, err
	}
	cleanup, err := configure(dev, name, s)
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &Adapter{Device: dev, name: name, cleanup: cleanup}, nil
}
