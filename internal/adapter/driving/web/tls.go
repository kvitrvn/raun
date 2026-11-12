package web

import (
	"crypto/tls"
	"errors"
	"fmt"
	"sync/atomic"
)

// Certificates holds the TLS certificate the server presents, and replaces
// it on Reload without stopping the server: an ACME client renews the files
// and sends SIGHUP.
type Certificates struct {
	certFile string
	keyFile  string
	current  atomic.Pointer[tls.Certificate]
}

// LoadCertificates loads the pair of PEM files certFile and keyFile.
func LoadCertificates(certFile, keyFile string) (*Certificates, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("load TLS pair: both files are needed")
	}
	c := &Certificates{certFile: certFile, keyFile: keyFile}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// Reload loads the pair again from its files. On failure the pair served so
// far is kept and the error is returned; it names the files, never their
// content.
func (c *Certificates) Reload() error {
	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS pair %s and %s: %w", c.certFile, c.keyFile, err)
	}
	c.current.Store(&pair)
	return nil
}

// TLSConfig returns the configuration of a server presenting the current
// pair, with TLS 1.2 at least.
func (c *Certificates) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return c.current.Load(), nil
		},
	}
}
