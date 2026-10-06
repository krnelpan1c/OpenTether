package mobile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/krnelpan1c/OpenTether/core/pairing"
	"github.com/krnelpan1c/OpenTether/core/relay"
)

type fakePlatform struct{ logs []string }

func (f *fakePlatform) BindSocket(int32) bool      { return true }
func (f *fakePlatform) ResolveRaw(q []byte) []byte { return q }
func (f *fakePlatform) HasIPv6() bool              { return false }
func (f *fakePlatform) Log(_ int32, msg string)    { f.logs = append(f.logs, msg) }

func TestServiceIdentityAndPairing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir, "Pixel", &fakePlatform{})
	if err != nil {
		t.Fatal(err)
	}
	tok := s.Token()
	if len(tok) != 19 || strings.Count(tok, "-") != 3 {
		t.Fatalf("unexpected token format %q", tok)
	}

	// Identity and token survive a restart.
	s2, err := NewService(dir, "Pixel", &fakePlatform{})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Fingerprint() != s.Fingerprint() || s2.Token() != tok {
		t.Fatal("identity or token not persisted")
	}

	uri := s.PairingURI("Pixel", "192.168.49.1", 47100, "DIRECT-ab-OpenTether", "hunter22")
	p, err := pairing.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if p.Token != tok || p.Fingerprint != s.Fingerprint() || p.Port != 47100 {
		t.Fatalf("bad pairing %+v", p)
	}

	newTok, err := s.RotateToken()
	if err != nil || newTok == tok || s.Token() != newTok {
		t.Fatalf("rotate failed: %v", err)
	}

	var st relay.Stats
	if err := json.Unmarshal([]byte(s.StatsJSON()), &st); err != nil {
		t.Fatal(err)
	}
}
