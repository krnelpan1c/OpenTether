package osnet

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

const scutilKey = "State:/Network/Service/OpenTether/DNS"

func createTUN(s Settings) (tun.Device, error) {
	dev, err := tun.CreateTUN("utun", s.MTU)
	if err != nil {
		return nil, fmt.Errorf("creating the utun device needs root (run with sudo): %w", err)
	}
	return dev, nil
}

func run(stdin string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func configure(_ tun.Device, name string, s Settings) (func(), error) {
	cmds := [][]string{
		// utun is point-to-point; the virtual resolver doubles as the peer.
		{"ifconfig", name, "inet", s.Addr4.Addr().String(), s.DNS4.String(), "netmask", "255.255.255.252", "mtu", fmt.Sprint(s.MTU), "up"},
		{"ifconfig", name, "inet6", s.Addr6.Addr().String(), "prefixlen", fmt.Sprint(s.Addr6.Bits())},
	}
	for _, r := range routes4 {
		cmds = append(cmds, []string{"route", "-q", "-n", "add", "-inet", r.String(), "-interface", name})
	}
	for _, r := range routes6 {
		cmds = append(cmds, []string{"route", "-q", "-n", "add", "-inet6", r.String(), "-interface", name})
	}
	for _, c := range cmds {
		if err := run("", c...); err != nil {
			return nil, err
		}
	}
	script := fmt.Sprintf("d.init\nd.add ServerAddresses * %s %s\nd.add SupplementalMatchDomains * \"\"\nset %s\n",
		s.DNS4, s.DNS6, scutilKey)
	if err := run(script, "scutil"); err != nil {
		slog.Warn("could not register DNS with scutil; set DNS to "+s.DNS4.String()+" manually", "err", err)
	}
	return func() { _ = run("remove "+scutilKey+"\n", "scutil") }, nil
}
