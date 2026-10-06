package osnet

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

func createTUN(s Settings) (tun.Device, error) {
	dev, err := tun.CreateTUN(s.Name, s.MTU)
	if err != nil {
		return nil, fmt.Errorf("creating the TUN device needs root or CAP_NET_ADMIN: %w", err)
	}
	return dev, nil
}

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func configure(_ tun.Device, name string, s Settings) (func(), error) {
	cmds := [][]string{
		{"ip", "addr", "add", s.Addr4.String(), "dev", name},
		{"ip", "-6", "addr", "add", s.Addr6.String(), "dev", name, "nodad"},
		{"ip", "link", "set", "dev", name, "mtu", fmt.Sprint(s.MTU), "up"},
	}
	for _, r := range routes4 {
		cmds = append(cmds, []string{"ip", "route", "add", r.String(), "dev", name})
	}
	for _, r := range routes6 {
		cmds = append(cmds, []string{"ip", "-6", "route", "add", r.String(), "dev", name})
	}
	for _, c := range cmds {
		if err := run(c...); err != nil {
			return nil, err
		}
	}
	// DNS: systemd-resolved is the common case. Elsewhere the user points
	// /etc/resolv.conf at the virtual resolver.
	if err := run("resolvectl", "dns", name, s.DNS4.String(), s.DNS6.String()); err != nil {
		slog.Warn("could not set DNS via resolvectl; set your resolver to "+s.DNS4.String()+" manually", "err", err)
	} else if err := run("resolvectl", "domain", name, "~."); err != nil {
		slog.Warn("could not make the tunnel the default DNS route", "err", err)
	}
	// Deleting the interface removes its routes and resolved settings.
	return func() {}, nil
}
