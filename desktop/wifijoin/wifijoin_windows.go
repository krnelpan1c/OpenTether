package wifijoin

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func netsh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("netsh %s: %v: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func connect(ctx context.Context, ssid, passphrase string) error {
	profile := fmt.Sprintf(`<?xml version="1.0"?>
<WLANProfile xmlns="http://www.microsoft.com/networking/WLAN/profile/v1">
  <name>%[1]s</name>
  <SSIDConfig><SSID><name>%[1]s</name></SSID></SSIDConfig>
  <connectionType>ESS</connectionType>
  <connectionMode>manual</connectionMode>
  <MSM><security>
    <authEncryption><authentication>WPA2PSK</authentication><encryption>AES</encryption><useOneX>false</useOneX></authEncryption>
    <sharedKey><keyType>passPhrase</keyType><protected>false</protected><keyMaterial>%[2]s</keyMaterial></sharedKey>
  </security></MSM>
</WLANProfile>
`, xmlEscape(ssid), xmlEscape(passphrase))

	dir, err := os.MkdirTemp("", "opentether-wlan")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) // the file holds the password
	path := filepath.Join(dir, "profile.xml")
	if err := os.WriteFile(path, []byte(profile), 0o600); err != nil {
		return err
	}
	if _, err := netsh(ctx, "wlan", "add", "profile", "filename="+path, "user=current"); err != nil {
		return err
	}
	_, err = netsh(ctx, "wlan", "connect", "name="+ssid, "ssid="+ssid)
	return err
}

// The "Profile" line of `netsh wlan show interfaces` names the profile in
// use. The label is localised, so on non-English Windows restoring the
// previous network is skipped.
var profileLine = regexp.MustCompile(`(?m)^\s*Profile\s*:\s*(.+?)\s*$`)

func currentNetwork() string {
	out, err := netsh(context.Background(), "wlan", "show", "interfaces")
	if err != nil {
		return ""
	}
	if m := profileLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

func reconnect(profile string) {
	_, _ = netsh(context.Background(), "wlan", "connect", "name="+profile)
}
