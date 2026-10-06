package pairing

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := Pairing{
		Name:        "Pixel 9",
		Host:        "192.168.49.1",
		Port:        47100,
		Token:       "K7Q2-M9XD",
		Fingerprint: strings.Repeat("ab", 32),
		SSID:        "DIRECT-ot-OpenTether",
		Passphrase:  "pass word&=?",
	}
	got, err := Parse(p.URI())
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("got %+v want %+v", got, p)
	}
}

func TestRejectsIncomplete(t *testing.T) {
	for _, uri := range []string{
		"https://example.com",
		"opentether://pair?h=1.2.3.4&p=1",
		"opentether://pair?h=1.2.3.4&p=0&t=x&fp=" + strings.Repeat("a", 64),
	} {
		if _, err := Parse(uri); err == nil {
			t.Errorf("%q: expected error", uri)
		}
	}
}
