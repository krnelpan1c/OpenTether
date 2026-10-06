// Package pairing encodes the information a desktop needs to connect to a
// phone over Wi-Fi Direct. The phone shows it as a QR code and as text the
// user can paste.
package pairing

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Scheme is the URI scheme used for pairing links.
const Scheme = "opentether"

// Pairing describes how to reach and authenticate a phone.
type Pairing struct {
	Name        string `json:"name,omitempty"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	// SSID and Passphrase of the phone's Wi-Fi Direct group, so the desktop
	// can join it.
	SSID       string `json:"ssid,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// Addr returns host:port.
func (p Pairing) Addr() string { return net.JoinHostPort(p.Host, strconv.Itoa(p.Port)) }

// URI encodes p as an opentether://pair link.
func (p Pairing) URI() string {
	q := url.Values{}
	q.Set("h", p.Host)
	q.Set("p", strconv.Itoa(p.Port))
	q.Set("t", p.Token)
	q.Set("fp", p.Fingerprint)
	if p.Name != "" {
		q.Set("n", p.Name)
	}
	if p.SSID != "" {
		q.Set("s", p.SSID)
	}
	if p.Passphrase != "" {
		q.Set("k", p.Passphrase)
	}
	return Scheme + "://pair?" + q.Encode()
}

// Parse decodes a link produced by URI.
func Parse(uri string) (Pairing, error) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil {
		return Pairing{}, err
	}
	if u.Scheme != Scheme || u.Host != "pair" {
		return Pairing{}, errors.New("not an opentether://pair link")
	}
	q := u.Query()
	p := Pairing{
		Name:        q.Get("n"),
		Host:        q.Get("h"),
		Token:       q.Get("t"),
		Fingerprint: strings.ToLower(q.Get("fp")),
		SSID:        q.Get("s"),
		Passphrase:  q.Get("k"),
	}
	if p.Port, err = strconv.Atoi(q.Get("p")); err != nil || p.Port <= 0 || p.Port > 65535 {
		return Pairing{}, fmt.Errorf("invalid port %q", q.Get("p"))
	}
	if p.Host == "" || p.Token == "" || len(p.Fingerprint) != 64 {
		return Pairing{}, errors.New("pairing link is missing the host, token or fingerprint")
	}
	return p, nil
}
