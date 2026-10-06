// Package config persists desktop-side state: pinned phone certificates
// and the last Wi-Fi Direct pairing.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/krnelpan1c/OpenTether/core/pairing"
)

// Store is a JSON file in the user's config directory.
type Store struct {
	path string

	mu   sync.Mutex
	data storeData
}

type storeData struct {
	// Pins maps a link key ("usb:<serial>") to the phone's certificate
	// fingerprint, recorded on first connection.
	Pins map[string]string `json:"pins"`
	// Wifi is the most recent Wi-Fi Direct pairing.
	Wifi *pairing.Pairing `json:"wifi,omitempty"`
}

// DefaultPath returns the store location for this user.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "OpenTether", "desktop.json"), nil
}

// Open loads the store at path; a missing file is an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: storeData{Pins: map[string]string{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Pins == nil {
		s.data.Pins = map[string]string{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// Pin returns the stored fingerprint for key.
func (s *Store) Pin(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Pins[key]
}

// SetPin records the fingerprint for key.
func (s *Store) SetPin(key, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Pins[key] = fp
	return s.saveLocked()
}

// Wifi returns the last Wi-Fi pairing, if any.
func (s *Store) Wifi() *pairing.Pairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Wifi == nil {
		return nil
	}
	p := *s.data.Wifi
	return &p
}

// SetWifi stores the Wi-Fi pairing.
func (s *Store) SetWifi(p pairing.Pairing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Wifi = &p
	return s.saveLocked()
}

// Forget removes all pins and pairings.
func (s *Store) Forget() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = storeData{Pins: map[string]string{}}
	return s.saveLocked()
}
