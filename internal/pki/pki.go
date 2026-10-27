package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Generate creates a new directory containing a private CA and one server/client
// identity. An existing directory is never overwritten.
func Generate(dir string, hosts []string, days int) error {
	if days < 1 || days > 3650 {
		return errors.New("days must be between 1 and 3650")
	}
	var dnsNames []string
	var ips []net.IP
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			ips = append(ips, ip)
		} else {
			if strings.ContainsAny(host, "/: \t\r\n") {
				return fmt.Errorf("invalid host %q: use a DNS name or IP without a port", host)
			}
			dnsNames = append(dnsNames, host)
		}
	}
	if len(dnsNames)+len(ips) == 0 {
		return errors.New("at least one server hostname or IP is required")
	}
	newKey := func() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }
	caKey, err := newKey()
	if err != nil {
		return err
	}
	now := time.Now()
	template := func(name string) (*x509.Certificate, error) {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		return &x509.Certificate{
			SerialNumber: new(big.Int).Add(serial, big.NewInt(1)),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.Add(time.Duration(days) * 24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			BasicConstraintsValid: true,
		}, nil
	}
	ca, err := template("revtcp CA")
	if err != nil {
		return err
	}
	ca.IsCA = true
	ca.MaxPathLenZero = true
	ca.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	files := make(map[string][]byte)
	add := func(name string, cert, issuer *x509.Certificate, key, issuerKey *ecdsa.PrivateKey) error {
		der, err := x509.CreateCertificate(rand.Reader, cert, issuer, &key.PublicKey, issuerKey)
		if err != nil {
			return err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		files[name+".crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		files[name+".key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		return nil
	}
	if err := add("ca", ca, ca, caKey, caKey); err != nil {
		return err
	}
	for _, name := range []string{"server", "client"} {
		key, err := newKey()
		if err != nil {
			return err
		}
		cert, err := template(name)
		if err != nil {
			return err
		}
		if name == "server" {
			cert.DNSNames, cert.IPAddresses = dnsNames, ips
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		} else {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		if err := add(name, cert, ca, key, caKey); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create new certificate directory (must not exist): %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(dir)
		}
	}()
	for name, data := range files {
		mode := os.FileMode(0600)
		if strings.HasSuffix(name, ".crt") {
			mode = 0644
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return err
		}
	}
	complete = true
	return nil
}
