// Package tlsutil: samopodpisany certyfikat serwera + przypinanie odcisku
// (trust-on-first-use) po stronie klienta, podobnie jak w SSH.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
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
)

// Fingerprint zwraca SHA-256 certyfikatu w formacie AA:BB:...
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// LoadOrCreate wczytuje cert.pem/key.pem z katalogu lub generuje nowe (ECDSA P-256, 20 lat).
func LoadOrCreate(dir, commonName string) (tls.Certificate, string, error) {
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(certPath, keyPath, commonName); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, Fingerprint(cert.Certificate[0]), nil
}

func generate(certPath, keyPath, cn string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"USBShare"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
}

// ErrFingerprintMismatch – certyfikat serwera inny niż zapamiętany.
type ErrFingerprintMismatch struct{ Expected, Got string }

func (e *ErrFingerprintMismatch) Error() string {
	return fmt.Sprintf("odcisk certyfikatu serwera się zmienił (oczekiwano %s, jest %s)", e.Expected, e.Got)
}

// ClientConfig weryfikuje serwer po przypiętym odcisku. Gdy expected jest pusty,
// akceptuje dowolny certyfikat (pierwsze połączenie), a odcisk zapisuje w *seen.
func ClientConfig(expected string, seen *string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // weryfikacja odbywa się w VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("serwer nie przedstawił certyfikatu")
			}
			fp := Fingerprint(cs.PeerCertificates[0].Raw)
			if seen != nil {
				*seen = fp
			}
			if expected != "" && !strings.EqualFold(expected, fp) {
				return &ErrFingerprintMismatch{Expected: expected, Got: fp}
			}
			return nil
		},
	}
}
