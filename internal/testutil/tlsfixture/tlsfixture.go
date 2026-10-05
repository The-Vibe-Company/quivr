// Package tlsfixture supplies ephemeral certificates for adapter handshake tests.
package tlsfixture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type Certificates struct {
	CAFile, CertFile, KeyFile string
	Server                    tls.Certificate
	Roots                     *x509.CertPool
}

func New(t testing.TB) Certificates {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "adapter test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"dependency.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, block *pem.Block) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	f := Certificates{CAFile: write("ca.pem", &pem.Block{Type: "CERTIFICATE", Bytes: caDER}), CertFile: write("cert.pem", &pem.Block{Type: "CERTIFICATE", Bytes: der}), KeyFile: write("key.pem", &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}
	f.Server, err = tls.LoadX509KeyPair(f.CertFile, f.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	f.Roots = x509.NewCertPool()
	f.Roots.AddCert(ca)
	return f
}
