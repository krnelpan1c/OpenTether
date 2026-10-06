// Package wifijoin connects the computer to the phone's Wi-Fi Direct
// network (which looks like an ordinary WPA2 network to the computer) and
// reconnects the previous network afterwards.
package wifijoin

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// ErrUnsupported means automatic joining is not available on this system.
var ErrUnsupported = errors.New("joining Wi-Fi networks automatically is not supported here")

// Joined reports whether this computer already has an address in prefix,
// i.e. it is connected to the phone's group.
func Joined(prefix netip.Prefix) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip, ok := netip.AddrFromSlice(ipn.IP); ok && prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// Join connects to ssid and waits until the computer has an address in
// prefix. The returned restore function reconnects whatever Wi-Fi network
// was in use before (best effort).
func Join(ctx context.Context, ssid, passphrase string, prefix netip.Prefix) (restore func(), err error) {
	previous := currentNetwork()
	if err := connect(ctx, ssid, passphrase); err != nil {
		return func() {}, err
	}
	restore = func() {
		if previous != "" && previous != ssid {
			reconnect(previous)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for !Joined(prefix) {
		if time.Now().After(deadline) {
			return restore, errors.New("joined the network but got no address from the phone")
		}
		select {
		case <-ctx.Done():
			return restore, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return restore, nil
}
