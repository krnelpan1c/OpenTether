// Package adb drives the Android Debug Bridge to forward a local TCP port to
// the OpenTether app's abstract socket on a USB-connected phone.
package adb

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ErrNotFound is returned when no adb executable can be located.
var ErrNotFound = errors.New("adb not found: install Android platform-tools " +
	"(https://developer.android.com/tools/releases/platform-tools) or add adb to PATH")

// Device is a phone known to adb.
type Device struct {
	Serial string
	State  string // "device", "unauthorized", "offline", ...
	Model  string
}

// Find locates the adb executable.
func Find() (string, error) {
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}
	exe := "adb"
	if runtime.GOOS == "windows" {
		exe = "adb.exe"
	}
	var candidates []string
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(env); v != "" {
			candidates = append(candidates, filepath.Join(v, "platform-tools", exe))
		}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates, filepath.Join(os.Getenv("LOCALAPPDATA"), "Android", "Sdk", "platform-tools", exe))
	case "darwin":
		candidates = append(candidates, filepath.Join(home, "Library", "Android", "sdk", "platform-tools", exe))
	default:
		candidates = append(candidates, filepath.Join(home, "Android", "Sdk", "platform-tools", exe))
	}
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "platform-tools", exe))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", ErrNotFound
}

// Client runs adb commands.
type Client struct {
	Path string
}

func (c Client) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	hideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("adb %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}

// Devices lists connected phones.
func (c Client) Devices(ctx context.Context) ([]Device, error) {
	out, err := c.run(ctx, "devices", "-l")
	if err != nil {
		return nil, err
	}
	return parseDevices(out), nil
}

func parseDevices(out string) []Device {
	var devs []Device
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := Device{Serial: fields[0], State: fields[1]}
		for _, f := range fields[2:] {
			if m, ok := strings.CutPrefix(f, "model:"); ok {
				d.Model = strings.ReplaceAll(m, "_", " ")
			}
		}
		devs = append(devs, d)
	}
	return devs
}

// PickDevice returns the device to use: the one matching serial, or the only
// ready device when serial is empty.
func (c Client) PickDevice(ctx context.Context, serial string) (Device, error) {
	devs, err := c.Devices(ctx)
	if err != nil {
		return Device{}, err
	}
	var ready []Device
	for _, d := range devs {
		if serial != "" && d.Serial != serial {
			continue
		}
		switch d.State {
		case "device":
			ready = append(ready, d)
		case "unauthorized":
			return Device{}, fmt.Errorf("phone %s has not authorised this computer: unlock it and accept the USB debugging prompt", d.Serial)
		}
	}
	switch {
	case len(ready) == 1:
		return ready[0], nil
	case len(ready) == 0 && serial != "":
		return Device{}, fmt.Errorf("phone %s is not connected", serial)
	case len(ready) == 0:
		return Device{}, errors.New("no phone found: connect it over USB and enable USB debugging")
	default:
		return Device{}, errors.New("several phones are connected: choose one with --serial")
	}
}

// Forward forwards a free local TCP port to the abstract socket name on the
// phone and returns the port.
func (c Client) Forward(ctx context.Context, serial, name string) (int, error) {
	out, err := c.run(ctx, "-s", serial, "forward", "tcp:0", "localabstract:"+name)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("adb forward returned %q", strings.TrimSpace(out))
	}
	return port, nil
}

// RemoveForward undoes Forward.
func (c Client) RemoveForward(ctx context.Context, serial string, port int) error {
	_, err := c.run(ctx, "-s", serial, "forward", "--remove", "tcp:"+strconv.Itoa(port))
	return err
}
