package wifijoin

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Linux support uses NetworkManager's nmcli, present on most desktops.

func nmcli(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("nmcli"); err != nil {
		return "", ErrUnsupported
	}
	out, err := exec.CommandContext(ctx, "nmcli", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("nmcli: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func connect(ctx context.Context, ssid, passphrase string) error {
	_, _ = nmcli(ctx, "device", "wifi", "rescan", "ssid", ssid)
	_, err := nmcli(ctx, "device", "wifi", "connect", ssid, "password", passphrase)
	return err
}

func currentNetwork() string {
	out, err := nmcli(context.Background(), "-t", "-f", "NAME,TYPE", "connection", "show", "--active")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if name, typ, ok := strings.Cut(line, ":"); ok && strings.HasSuffix(typ, "wireless") {
			return name
		}
	}
	return ""
}

func reconnect(name string) {
	_, _ = nmcli(context.Background(), "connection", "up", "id", name)
}
