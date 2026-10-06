// Command opentether-relay runs the phone-side relay on an ordinary
// computer. It is meant for development: point the desktop client at it to
// exercise the whole pipeline without an Android device.
//
//	opentether-relay --tcp 127.0.0.1:47101 --udp :47100
//
// The TCP listener speaks the same framed protocol as the phone's adb socket
// (connect with `opentether usb --endpoint 127.0.0.1:47101`); the UDP
// listener behaves like Wi-Fi Direct mode and prints a pairing link for
// `opentether wifi --pair`.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/krnelpan1c/OpenTether/core/pairing"
	"github.com/krnelpan1c/OpenTether/core/relay"
	"github.com/krnelpan1c/OpenTether/core/session"
)

func main() {
	tcpAddr := flag.String("tcp", "127.0.0.1:47101", "stream listener (adb-style, trusted); empty to disable")
	udpAddr := flag.String("udp", ":47100", "QUIC/UDP listener (Wi-Fi Direct-style, needs token); empty to disable")
	host := flag.String("host", "", "address to advertise in the pairing link (default: first non-loopback IPv4)")
	identity := flag.String("identity", "", "identity PEM file (default: in the user config dir)")
	verbose := flag.Bool("verbose", false, "debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if *identity == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			fatal(err)
		}
		*identity = filepath.Join(dir, "OpenTether", "relay-identity.pem")
	}
	cert, err := session.LoadOrCreateIdentity(*identity, "opentether-relay")
	if err != nil {
		fatal(err)
	}
	token := newToken()
	name, _ := os.Hostname()
	srv := relay.New(relay.Config{
		Identity:   cert,
		ServerName: name + " (dev relay)",
		Token:      func() string { return token },
		Logger:     log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *tcpAddr != "" {
		ln, err := net.Listen("tcp", *tcpAddr)
		if err != nil {
			fatal(err)
		}
		log.Info("stream listener ready", "addr", ln.Addr())
		go func() { _ = srv.ServeStream(ctx, ln, true) }()
	}
	if *udpAddr != "" {
		pc, err := net.ListenPacket("udp", *udpAddr)
		if err != nil {
			fatal(err)
		}
		port := pc.LocalAddr().(*net.UDPAddr).Port
		h := *host
		if h == "" {
			h = firstIPv4()
		}
		p := pairing.Pairing{Name: name, Host: h, Port: port, Token: token, Fingerprint: session.CertFingerprint(cert)}
		fmt.Printf("Pairing link:\n  %s\n", p.URI())
		go func() { _ = srv.ServePacket(ctx, pc, false) }()
	}
	<-ctx.Done()
}

func newToken() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func firstIPv4() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
