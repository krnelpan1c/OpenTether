package osnet

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// adapterGUID keeps the adapter identity stable across runs so Windows does
// not create a new "network" profile every time.
var adapterGUID = windows.GUID{Data1: 0x0d7e7e7e, Data2: 0x07e7, Data3: 0x4e7e, Data4: [8]byte{0x8e, 0x7e, 0x70, 0x7e, 0x7e, 0x7e, 0x7e, 0x01}}

func createTUN(s Settings) (tun.Device, error) {
	dev, err := tun.CreateTUNWithRequestedGUID(s.Name, &adapterGUID, s.MTU)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("creating the virtual adapter needs administrator rights; run OpenTether as Administrator: %w", err)
		}
		return nil, fmt.Errorf("creating the Wintun adapter failed (is wintun.dll next to opentether.exe?): %w", err)
	}
	return dev, nil
}

func configure(dev tun.Device, _ string, s Settings) (func(), error) {
	nt, ok := dev.(*tun.NativeTun)
	if !ok {
		return nil, errors.New("unexpected TUN implementation")
	}
	luid := winipcfg.LUID(nt.LUID())

	for _, f := range []family{
		{"IPv4", windows.AF_INET, s.Addr4, s.DNS4, routes4, netip.IPv4Unspecified()},
		{"IPv6", windows.AF_INET6, s.Addr6, s.DNS6, routes6, netip.IPv6Unspecified()},
	} {
		// Windows registers each IP family on a new adapter a moment after
		// the adapter itself appears; configuring it earlier fails with
		// "Element not found".
		if err := waitForFamily(luid, f.af); err != nil {
			if f.af == windows.AF_INET6 {
				slog.Warn("IPv6 is unavailable on the virtual adapter; continuing with IPv4 only", "err", err)
				continue
			}
			return nil, fmt.Errorf("%s did not come up on the virtual adapter: %w", f.name, err)
		}
		if err := configureFamily(luid, f, s.MTU); err != nil {
			return nil, err
		}
	}
	// Removing the Wintun adapter on Close drops its addresses, routes and
	// DNS servers, so there is nothing else to undo.
	return func() {}, nil
}

type family struct {
	name   string
	af     winipcfg.AddressFamily
	addr   netip.Prefix
	dns    netip.Addr
	routes []netip.Prefix
	zero   netip.Addr
}

func configureFamily(luid winipcfg.LUID, f family, mtu int) error {
	iface, err := luid.IPInterface(f.af)
	if err != nil {
		return fmt.Errorf("read %s interface: %w", f.name, err)
	}
	// Lowest metric so Windows prefers our DNS server and routes. With
	// duplicate address detection off, the address is usable immediately.
	iface.UseAutomaticMetric = false
	iface.Metric = 0
	iface.NLMTU = uint32(mtu)
	iface.DadTransmits = 0
	iface.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
	if err := retryNotFound(iface.Set); err != nil {
		return fmt.Errorf("configure %s interface: %w", f.name, err)
	}
	if err := retryNotFound(func() error {
		return luid.SetIPAddressesForFamily(f.af, []netip.Prefix{f.addr})
	}); err != nil {
		return fmt.Errorf("set %s address: %w", f.name, err)
	}
	var routes []*winipcfg.RouteData
	for _, r := range f.routes {
		routes = append(routes, &winipcfg.RouteData{Destination: r, NextHop: f.zero, Metric: 0})
	}
	if err := retryNotFound(func() error { return luid.SetRoutesForFamily(f.af, routes) }); err != nil {
		return fmt.Errorf("set %s routes: %w", f.name, err)
	}
	if err := retryNotFound(func() error { return luid.SetDNS(f.af, []netip.Addr{f.dns}, nil) }); err != nil {
		return fmt.Errorf("set %s DNS: %w", f.name, err)
	}
	return nil
}

// waitForFamily waits until the adapter has an interface for af.
func waitForFamily(luid winipcfg.LUID, af winipcfg.AddressFamily) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := luid.IPInterface(af)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// retryNotFound retries op while Windows is still finishing adapter setup.
func retryNotFound(op func() error) error {
	var err error
	for range 30 {
		if err = op(); !errors.Is(err, windows.ERROR_NOT_FOUND) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
