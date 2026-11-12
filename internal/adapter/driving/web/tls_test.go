package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a new self-signed pair for raun.example to certFile and
// keyFile, and returns its serial number.
func writePair(t *testing.T, certFile, keyFile string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: testHost},
		DNSNames:     []string{testHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// servedSerial connects to the TLS server at url and returns the serial
// number of the certificate it presents.
func servedSerial(t *testing.T, url string) int64 {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: testHost},
		DisableKeepAlives: true,
	}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.TLS.PeerCertificates[0].SerialNumber.Int64()
}

func TestCertificatesReload(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writePair(t, certFile, keyFile, 1)

	certs, err := LoadCertificates(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = certs.TLSConfig()
	srv.StartTLS()
	defer srv.Close()

	if got := servedSerial(t, srv.URL); got != 1 {
		t.Fatalf("serial = %d, want 1", got)
	}
	if v := certs.TLSConfig().MinVersion; v != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", v)
	}

	writePair(t, certFile, keyFile, 2)
	if err := certs.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := servedSerial(t, srv.URL); got != 2 {
		t.Fatalf("serial after reload = %d, want 2", got)
	}

	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := certs.Reload(); err == nil {
		t.Fatal("Reload of a broken pair succeeded")
	}
	if got := servedSerial(t, srv.URL); got != 2 {
		t.Fatalf("serial after a failed reload = %d, want 2", got)
	}
}

func TestLoadCertificatesRefusesBadPairs(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writePair(t, certFile, keyFile, 1)
	otherCert, otherKey := filepath.Join(dir, "other.pem"), filepath.Join(dir, "other-key.pem")
	writePair(t, otherCert, otherKey, 2)

	tests := []struct{ name, cert, key string }{
		{"no certificate", "", keyFile},
		{"no key", certFile, ""},
		{"missing file", filepath.Join(dir, "missing.pem"), keyFile},
		{"key of another pair", certFile, otherKey},
		{"key given as certificate", keyFile, keyFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadCertificates(tt.cert, tt.key); err == nil {
				t.Fatal("LoadCertificates accepted the pair")
			}
		})
	}
}
