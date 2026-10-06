package session

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/krnelpan1c/OpenTether/core/proto"
)

// ErrFingerprintMismatch is returned when the server certificate does not
// match the pinned fingerprint.
var ErrFingerprintMismatch = errors.New("server certificate does not match the paired fingerprint")

// GenerateIdentity creates a self-signed ECDSA P-256 certificate and returns
// it PEM-encoded (certificate block followed by key block).
func GenerateIdentity(commonName string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"opentether.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
	return out, nil
}

// LoadOrCreateIdentity loads the PEM identity stored at path, creating it on
// first use.
func LoadOrCreateIdentity(path, commonName string) (tls.Certificate, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if data, err = GenerateIdentity(commonName); err != nil {
			return tls.Certificate{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return tls.Certificate{}, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return tls.Certificate{}, err
		}
	} else if err != nil {
		return tls.Certificate{}, err
	}
	return ParseIdentity(data)
}

// ParseIdentity parses an identity produced by GenerateIdentity.
func ParseIdentity(pemData []byte) (tls.Certificate, error) {
	return tls.X509KeyPair(pemData, pemData)
}

// Fingerprint returns the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// CertFingerprint returns the fingerprint of the leaf certificate.
func CertFingerprint(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	return Fingerprint(c.Certificate[0])
}

// ShortFingerprint formats the first bytes of a fingerprint for humans to
// compare, e.g. "3F9A-27C1-0B44".
func ShortFingerprint(fp string) string {
	fp = strings.ToUpper(fp)
	if len(fp) < 12 {
		return fp
	}
	return fp[0:4] + "-" + fp[4:8] + "-" + fp[8:12]
}

// ServerTLSConfig returns the TLS configuration used by the phone relay.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{proto.ALPN},
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLSConfig returns a TLS configuration that authenticates the server
// by certificate fingerprint instead of a CA chain. If pin is empty, any
// certificate is accepted (trust on first use) and its fingerprint is
// stored in *seen so the caller can pin it.
func ClientTLSConfig(pin string, seen *string) *tls.Config {
	pin = strings.ToLower(strings.TrimSpace(pin))
	return &tls.Config{
		NextProtos:         []string{proto.ALPN},
		MinVersion:         tls.VersionTLS13,
		ServerName:         "opentether.local",
		InsecureSkipVerify: true, // verified by fingerprint below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("server presented no certificate")
			}
			fp := Fingerprint(rawCerts[0])
			if pin != "" && subtle.ConstantTimeCompare([]byte(fp), []byte(pin)) != 1 {
				return fmt.Errorf("%w (expected %s, got %s)", ErrFingerprintMismatch,
					ShortFingerprint(pin), ShortFingerprint(fp))
			}
			if seen != nil {
				*seen = fp
			}
			return nil
		},
	}
}
