package wifijoin

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// wifiDevice finds the Wi-Fi interface (usually en0).
func wifiDevice() string {
	out, err := exec.Command("networksetup", "-listallhardwareports").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if strings.Contains(l, "Wi-Fi") && i+1 < len(lines) {
			if dev, ok := strings.CutPrefix(strings.TrimSpace(lines[i+1]), "Device: "); ok {
				return dev
			}
		}
	}
	return ""
}

func connect(ctx context.Context, ssid, passphrase string) error {
	dev := wifiDevice()
	if dev == "" {
		return ErrUnsupported
	}
	out, err := exec.CommandContext(ctx, "networksetup", "-setairportnetwork", dev, ssid, passphrase).CombinedOutput()
	// networksetup exits 0 even on some failures and prints the reason.
	if msg := strings.TrimSpace(string(out)); err != nil || strings.Contains(msg, "Could not") || strings.Contains(msg, "Error") {
		return fmt.Errorf("networksetup: %v %s", err, msg)
	}
	return nil
}

func currentNetwork() string {
	dev := wifiDevice()
	if dev == "" {
		return ""
	}
	out, err := exec.Command("networksetup", "-getairportnetwork", dev).Output()
	if err != nil {
		return ""
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "Current Wi-Fi Network: ")
	if !ok {
		return ""
	}
	return name
}

// reconnect relies on macOS rejoining a known network on its own once the
// phone's group is gone; it needs the password to switch explicitly.
func reconnect(string) {}
