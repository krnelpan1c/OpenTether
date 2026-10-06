// Command opentether is the desktop client. It creates a virtual network
// adapter and sends all of the computer's traffic through an OpenTether
// phone over USB (adb) or Wi-Fi Direct.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"syscall"
	"time"

	"github.com/krnelpan1c/OpenTether/core/netstack"
	"github.com/krnelpan1c/OpenTether/core/pairing"
	"github.com/krnelpan1c/OpenTether/core/session"
	"github.com/krnelpan1c/OpenTether/desktop/adb"
	"github.com/krnelpan1c/OpenTether/desktop/app"
	"github.com/krnelpan1c/OpenTether/desktop/config"
	"github.com/krnelpan1c/OpenTether/desktop/usb"
)

var version = "dev"

const usage = `OpenTether desktop client %s

Usage:
  opentether usb   [--serial SERIAL]      share the phone's connection over USB (needs USB debugging)
  opentether wifi  [--pair LINK]          share it over Wi-Fi Direct (joins the phone's network)
  opentether devices                      list phones visible to adb
  opentether forget                       forget paired phones
  opentether version

Common flags:
  --stats      print traffic counters every few seconds
  --verbose    debug logging

The virtual adapter needs administrator rights (Windows: run as Administrator;
Linux/macOS: run with sudo).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "usb":
		err = runUSB(args)
	case "wifi":
		err = runWifi(args)
	case "devices":
		err = listDevices(args)
	case "forget":
		err = forget()
	case "version", "--version", "-v":
		fmt.Println("opentether", version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type commonFlags struct {
	stats   bool
	verbose bool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&c.stats, "stats", false, "print traffic counters every few seconds")
	fs.BoolVar(&c.verbose, "verbose", false, "debug logging")
}

func (c *commonFlags) logger() *slog.Logger {
	level := slog.LevelInfo
	if c.verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func openStore() (*config.Store, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	return config.Open(path)
}

func runUSB(args []string) error {
	fs := flag.NewFlagSet("usb", flag.ExitOnError)
	var common commonFlags
	common.register(fs)
	serial := fs.String("serial", "", "adb serial of the phone (when several are connected)")
	endpoint := fs.String("endpoint", "", "connect to a relay's stream listener directly instead of via adb (development)")
	aoa := fs.Bool("experimental-aoa", false, "use Android Open Accessory instead of adb (experimental)")
	hideFlags(fs, "experimental-aoa")
	_ = fs.Parse(args)

	store, err := openStore()
	if err != nil {
		return err
	}
	switch {
	case *endpoint != "":
		return run(&app.StreamLink{Addr: *endpoint}, store, common)
	case *aoa:
		fmt.Println("Experimental: using USB accessory mode instead of adb. On Windows this needs UsbDk and USB debugging turned off.")
		return run(&app.AOALink{Log: common.logger()}, store, common)
	}
	adbPath, err := adb.Find()
	if err != nil {
		return err
	}
	return run(&app.USBLink{ADB: adb.Client{Path: adbPath}, Serial: *serial}, store, common)
}

// hideFlags keeps experimental flags working but leaves them out of -h.
func hideFlags(fs *flag.FlagSet, names ...string) {
	fs.Usage = func() {
		visible := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
		visible.SetOutput(fs.Output())
		fs.VisitAll(func(f *flag.Flag) {
			if !slices.Contains(names, f.Name) {
				visible.Var(f.Value, f.Name, f.Usage)
			}
		})
		fmt.Fprintf(fs.Output(), "Usage of opentether %s:\n", fs.Name())
		visible.PrintDefaults()
	}
}

func runWifi(args []string) error {
	fs := flag.NewFlagSet("wifi", flag.ExitOnError)
	var common commonFlags
	common.register(fs)
	link := fs.String("pair", "", "pairing link shown by the phone (opentether://pair?...)")
	noJoin := fs.Bool("no-join", false, "don't join the phone's Wi-Fi network automatically")
	_ = fs.Parse(args)

	store, err := openStore()
	if err != nil {
		return err
	}
	var p pairing.Pairing
	switch {
	case *link != "":
		if p, err = pairing.Parse(*link); err != nil {
			return err
		}
		if err := store.SetWifi(p); err != nil {
			return err
		}
	case store.Wifi() != nil:
		p = *store.Wifi()
	default:
		return errors.New("no phone paired yet: run `opentether wifi --pair \"<link from the phone>\"`")
	}
	if p.SSID != "" && *noJoin {
		fmt.Printf("Join the Wi-Fi network %q (password %q) if you have not already.\n", p.SSID, p.Passphrase)
	}
	return run(&app.WifiLink{Pairing: p, AutoJoin: !*noJoin, Log: common.logger()}, store, common)
}

func run(link app.Link, store *config.Store, common commonFlags) error {
	log := common.logger()
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := app.Options{
		Link:       link,
		Store:      store,
		ClientName: app.Hostname(),
		Logger:     log,
		OnStatus: func(s app.Status) {
			switch {
			case s.Connected:
				ipv6 := "IPv4 only"
				if s.IPv6 {
					ipv6 = "IPv4 + IPv6"
				}
				fmt.Printf("● Connected to %s (%s). All traffic now goes through the phone. Press Ctrl+C to stop.\n", s.Phone, ipv6)
			case errors.Is(s.Err, session.ErrFingerprintMismatch):
				fmt.Printf("✕ %v\n  If you reinstalled the app, run `opentether forget` and pair again.\n", s.Err)
			case s.Err != nil:
				fmt.Printf("○ Waiting for phone: %v\n", s.Err)
			}
		},
	}
	if common.stats {
		var last netstack.Stats
		lastAt := time.Now()
		opts.Stats = func(s netstack.Stats) {
			secs := time.Since(lastAt).Seconds()
			fmt.Printf("  ↑ %s/s  ↓ %s/s   tcp %d  udp %d  dns %d\n",
				rate(s.BytesUp-last.BytesUp, secs), rate(s.BytesDown-last.BytesDown, secs),
				s.TCPActive, s.UDPFlows, s.DNSQueries)
			last, lastAt = s, time.Now()
		}
	}
	err := app.Run(ctx, opts)
	fmt.Println("Stopped; network settings restored.")
	return err
}

func rate(bytes int64, secs float64) string {
	if secs <= 0 {
		secs = 1
	}
	bps := float64(bytes) / secs
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", bps)
	}
}

func listDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	aoa := fs.Bool("experimental-aoa", false, "also list phones reachable in USB accessory mode (experimental)")
	verbose := fs.Bool("verbose", false, "print libusb's own debug log (with --experimental-aoa)")
	hideFlags(fs, "experimental-aoa", "verbose")
	_ = fs.Parse(args)
	usb.Debug = *verbose

	if *aoa {
		listAccessoryDevices()
	}
	return listADBDevices()
}

func listAccessoryDevices() {
	fmt.Println("USB accessory mode (experimental):")
	if ctx, err := usb.Open(); err != nil {
		fmt.Printf("  unavailable: %v\n", err)
	} else {
		found := false
		for _, d := range ctx.Devices() {
			switch {
			case d.Accessory:
				fmt.Printf("  %s  in accessory mode\n", d)
			case d.Phone:
				fmt.Printf("  %s  phone\n", d)
			default:
				continue
			}
			found = true
		}
		if !found {
			fmt.Println("  no phones found")
		}
		if runtime.GOOS == "windows" {
			switch {
			case ctx.UsbDk:
				fmt.Println("  UsbDk: active")
			case !usb.UsbDkInstalled():
				fmt.Println("  UsbDk: not installed, so Windows can only reach phones through adb")
			default:
				fmt.Printf("  UsbDk: installed but libusb could not use it: %v\n", ctx.UsbDkErr)
			}
		}
		ctx.Close()
	}
}

func listADBDevices() error {
	fmt.Println("adb (USB debugging):")
	adbPath, err := adb.Find()
	if err != nil {
		fmt.Printf("  unavailable: %v\n", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	devs, err := adb.Client{Path: adbPath}.Devices(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("  no phones found")
	}
	for _, d := range devs {
		fmt.Printf("  %-24s %-14s %s\n", d.Serial, d.State, d.Model)
	}
	return nil
}

func forget() error {
	store, err := openStore()
	if err != nil {
		return err
	}
	if err := store.Forget(); err != nil {
		return err
	}
	fmt.Println("Forgot all paired phones.")
	return nil
}
