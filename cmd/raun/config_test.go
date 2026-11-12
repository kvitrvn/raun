package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePair writes a self-signed TLS pair for localhost with serial number 1
// into dir and returns its files.
func writePair(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	return writeSerialPair(t, dir, 1)
}

// writeSerialPair is writePair with the serial number of the certificate.
func writeSerialPair(t *testing.T, dir string, serial int64) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
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
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.httpAddr != ":8080" || c.publicURL.String() != "http://localhost:8080" || c.publicHost != "localhost:8080" ||
		c.tlsCertFile != "" || c.tlsKeyFile != "" || c.certificates != nil ||
		c.databasePath != "raun.db" || c.databaseMigrate || c.logLevel != slog.LevelInfo || c.logFormat != "json" ||
		c.shutdownTimeout != 10*time.Second {
		t.Errorf("defaults = %+v", c)
	}
	var names []string
	for _, s := range c.settings {
		names = append(names, s.name+"="+s.value)
	}
	want := "RAUN_HTTP_ADDR=:8080 RAUN_PUBLIC_URL=http://localhost:8080 RAUN_TLS_CERT_FILE= RAUN_TLS_KEY_FILE= " +
		"RAUN_DATABASE_PATH=raun.db RAUN_DATABASE_MIGRATE=false RAUN_LOG_LEVEL=info RAUN_LOG_FORMAT=json RAUN_SHUTDOWN_TIMEOUT=10s"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("settings = %s\nwant       %s", got, want)
	}
}

func TestLoadConfigValues(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir)
	tests := []struct {
		name  string
		env   []string
		check func(t *testing.T, c config)
	}{
		{"listen address", []string{"RAUN_HTTP_ADDR=127.0.0.1:1"}, func(t *testing.T, c config) {
			if c.httpAddr != "127.0.0.1:1" {
				t.Errorf("httpAddr = %q", c.httpAddr)
			}
		}},
		{"highest port", []string{"RAUN_HTTP_ADDR=[::1]:65535"}, func(t *testing.T, c config) {
			if c.httpAddr != "[::1]:65535" {
				t.Errorf("httpAddr = %q", c.httpAddr)
			}
		}},
		{"https public URL", []string{"RAUN_PUBLIC_URL=https://Raun.Example/"}, func(t *testing.T, c config) {
			if c.publicURL.String() != "https://Raun.Example" || c.publicHost != "raun.example" {
				t.Errorf("publicURL = %s, publicHost = %q", c.publicURL, c.publicHost)
			}
		}},
		{"https with its default port", []string{"RAUN_PUBLIC_URL=https://raun.example:443"}, func(t *testing.T, c config) {
			if c.publicHost != "raun.example" {
				t.Errorf("publicHost = %q", c.publicHost)
			}
		}},
		{"https on another port", []string{"RAUN_PUBLIC_URL=https://raun.example:8443"}, func(t *testing.T, c config) {
			if c.publicHost != "raun.example:8443" {
				t.Errorf("publicHost = %q", c.publicHost)
			}
		}},
		{"http on 127.0.0.1", []string{"RAUN_PUBLIC_URL=http://127.0.0.1:9090"}, func(t *testing.T, c config) {
			if c.publicHost != "127.0.0.1:9090" {
				t.Errorf("publicHost = %q", c.publicHost)
			}
		}},
		{"http on localhost, fully qualified", []string{"RAUN_PUBLIC_URL=http://localhost.:8080"}, func(t *testing.T, c config) {
			if c.publicHost != "localhost:8080" {
				t.Errorf("publicHost = %q", c.publicHost)
			}
		}},
		{"http on ::1", []string{"RAUN_PUBLIC_URL=http://[::1]"}, func(t *testing.T, c config) {
			if c.publicHost != "[::1]" {
				t.Errorf("publicHost = %q", c.publicHost)
			}
		}},
		{"TLS pair", []string{"RAUN_TLS_CERT_FILE=" + certFile, "RAUN_TLS_KEY_FILE=" + keyFile}, func(t *testing.T, c config) {
			if c.certificates == nil {
				t.Error("the TLS pair is not loaded")
			}
		}},
		{"database", []string{"RAUN_DATABASE_PATH=/var/lib/raun/raun.db", "RAUN_DATABASE_MIGRATE=true"}, func(t *testing.T, c config) {
			if c.databasePath != "/var/lib/raun/raun.db" || !c.databaseMigrate {
				t.Errorf("database = %q, migrate %v", c.databasePath, c.databaseMigrate)
			}
		}},
		{"logs", []string{"RAUN_LOG_LEVEL=debug", "RAUN_LOG_FORMAT=text"}, func(t *testing.T, c config) {
			if c.logLevel != slog.LevelDebug || c.logFormat != "text" {
				t.Errorf("log = %v %q", c.logLevel, c.logFormat)
			}
		}},
		{"shortest shutdown", []string{"RAUN_SHUTDOWN_TIMEOUT=1s"}, func(t *testing.T, c config) {
			if c.shutdownTimeout != time.Second {
				t.Errorf("shutdownTimeout = %s", c.shutdownTimeout)
			}
		}},
		{"longest shutdown", []string{"RAUN_SHUTDOWN_TIMEOUT=5m"}, func(t *testing.T, c config) {
			if c.shutdownTimeout != 5*time.Minute {
				t.Errorf("shutdownTimeout = %s", c.shutdownTimeout)
			}
		}},
		{"runner variables are ignored", []string{"RAUN_RUNNER_TOKEN=x", "RAUN_RUNNER_SERVER=https://raun.example"}, func(t *testing.T, c config) {}},
		{"other variables are ignored", []string{"HOME=/root", "PATH=/bin", "RAUNX=1", "NOEQUALS"}, func(t *testing.T, c config) {}},
		{"the first value wins", []string{"RAUN_LOG_LEVEL=warn", "RAUN_LOG_LEVEL=nonsense"}, func(t *testing.T, c config) {
			if c.logLevel != slog.LevelWarn {
				t.Errorf("logLevel = %v", c.logLevel)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadConfig(tt.env)
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			tt.check(t, c)
		})
	}
}

func TestLoadConfigRefuses(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir)
	broken := filepath.Join(dir, "broken.pem")
	if err := os.WriteFile(broken, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"unknown variable", "RAUN_FOO=bar", "RAUN_FOO: unknown variable"},
		{"variable of a later epic", "RAUN_ADMINS=github:octocat", "RAUN_ADMINS: unknown variable"},
		{"_FILE form of a variable that is not secret", "RAUN_LOG_LEVEL_FILE=/x", "RAUN_LOG_LEVEL_FILE: unknown variable"},
		{"address without a port", "RAUN_HTTP_ADDR=localhost", "RAUN_HTTP_ADDR:"},
		{"port 0", "RAUN_HTTP_ADDR=:0", "RAUN_HTTP_ADDR: port \"0\""},
		{"port 65536", "RAUN_HTTP_ADDR=:65536", "RAUN_HTTP_ADDR: port \"65536\""},
		{"port by name", "RAUN_HTTP_ADDR=:http", "RAUN_HTTP_ADDR: port \"http\""},
		{"empty address", "RAUN_HTTP_ADDR=", "RAUN_HTTP_ADDR:"},
		{"public URL without a scheme", "RAUN_PUBLIC_URL=raun.example", "RAUN_PUBLIC_URL:"},
		{"public URL of another scheme", "RAUN_PUBLIC_URL=ftp://raun.example", "RAUN_PUBLIC_URL:"},
		{"http on a public host", "RAUN_PUBLIC_URL=http://raun.example", "RAUN_PUBLIC_URL: \"http://raun.example\": http is allowed on a loopback address only"},
		{"http on a private address", "RAUN_PUBLIC_URL=http://10.0.0.7:8080", "RAUN_PUBLIC_URL:"},
		{"public URL with a path", "RAUN_PUBLIC_URL=https://raun.example/raun", "RAUN_PUBLIC_URL: \"https://raun.example/raun\" has a path"},
		{"public URL with a query", "RAUN_PUBLIC_URL=https://raun.example/?a=1", "RAUN_PUBLIC_URL:"},
		{"public URL with an empty query", "RAUN_PUBLIC_URL=https://raun.example?", "RAUN_PUBLIC_URL:"},
		{"public URL with a fragment", "RAUN_PUBLIC_URL=https://raun.example#top", "RAUN_PUBLIC_URL:"},
		{"public URL with user information", "RAUN_PUBLIC_URL=https://admin@raun.example", "RAUN_PUBLIC_URL:"},
		{"public URL without a host", "RAUN_PUBLIC_URL=https:///", "RAUN_PUBLIC_URL:"},
		{"public URL with port 0", "RAUN_PUBLIC_URL=https://raun.example:0", "RAUN_PUBLIC_URL:"},
		{"empty public URL", "RAUN_PUBLIC_URL=", "RAUN_PUBLIC_URL:"},
		{"certificate without a key", "RAUN_TLS_CERT_FILE=" + certFile, "RAUN_TLS_CERT_FILE and RAUN_TLS_KEY_FILE: set both or neither"},
		{"key without a certificate", "RAUN_TLS_KEY_FILE=" + keyFile, "RAUN_TLS_CERT_FILE and RAUN_TLS_KEY_FILE: set both or neither"},
		{"empty database path", "RAUN_DATABASE_PATH=", "RAUN_DATABASE_PATH: empty path"},
		{"in-memory database", "RAUN_DATABASE_PATH=:memory:", "RAUN_DATABASE_PATH:"},
		{"database URI", "RAUN_DATABASE_PATH=file:raun.db?mode=ro", "RAUN_DATABASE_PATH:"},
		{"migrate in capitals", "RAUN_DATABASE_MIGRATE=TRUE", "RAUN_DATABASE_MIGRATE: \"TRUE\" is not true or false"},
		{"migrate as a number", "RAUN_DATABASE_MIGRATE=1", "RAUN_DATABASE_MIGRATE:"},
		{"unknown log level", "RAUN_LOG_LEVEL=verbose", "RAUN_LOG_LEVEL: \"verbose\" is not debug, info, warn or error"},
		{"unknown log format", "RAUN_LOG_FORMAT=logfmt", "RAUN_LOG_FORMAT: \"logfmt\" is not json or text"},
		{"shutdown without a unit", "RAUN_SHUTDOWN_TIMEOUT=10", "RAUN_SHUTDOWN_TIMEOUT:"},
		{"shutdown of zero", "RAUN_SHUTDOWN_TIMEOUT=0s", "RAUN_SHUTDOWN_TIMEOUT: 0s is not from 1s to 5m0s"},
		{"shutdown just under the bound", "RAUN_SHUTDOWN_TIMEOUT=999ms", "RAUN_SHUTDOWN_TIMEOUT:"},
		{"shutdown just over the bound", "RAUN_SHUTDOWN_TIMEOUT=5m1s", "RAUN_SHUTDOWN_TIMEOUT: 5m1s is not from 1s to 5m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig([]string{tt.env})
			if err == nil {
				t.Fatal("loadConfig accepted the configuration")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}

	t.Run("a broken TLS pair", func(t *testing.T) {
		_, err := loadConfig([]string{"RAUN_TLS_CERT_FILE=" + certFile, "RAUN_TLS_KEY_FILE=" + broken})
		if err == nil || !strings.Contains(err.Error(), "RAUN_TLS_CERT_FILE and RAUN_TLS_KEY_FILE: load TLS pair") {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "not a key") {
			t.Fatalf("the error holds the content of the key file: %v", err)
		}
	})
}

func TestLoadConfigReportsEveryError(t *testing.T) {
	_, err := loadConfig([]string{"RAUN_ZZZ=1", "RAUN_LOG_LEVEL=verbose", "RAUN_AAA=1", "RAUN_HTTP_ADDR=:0"})
	if err == nil {
		t.Fatal("loadConfig accepted the configuration")
	}
	var names []string
	for _, line := range strings.Split(err.Error(), "\n") {
		name, _, _ := strings.Cut(line, ":")
		names = append(names, name)
	}
	if got := strings.Join(names, " "); got != "RAUN_AAA RAUN_HTTP_ADDR RAUN_LOG_LEVEL RAUN_ZZZ" {
		t.Errorf("errors in the order %s, want them sorted", got)
	}
}

func TestSecretsAreNeverShown(t *testing.T) {
	vars := append([]variable{{
		name: "RAUN_TEST_TOKEN", secret: true,
		parse: func(*config, string) error { return nil },
	}}, variables...)
	c, err := load([]string{"RAUN_TEST_TOKEN=token-sentinel"}, vars)
	if err != nil {
		t.Fatal(err)
	}
	attrs := c.logAttrs()
	if attrs[0].Key != "RAUN_TEST_TOKEN" || attrs[0].Value.String() != redacted {
		t.Errorf("first attribute = %v, want the token redacted", attrs[0])
	}
	for _, a := range attrs {
		if strings.Contains(a.Value.String(), "token-sentinel") {
			t.Errorf("%s shows the secret", a.Key)
		}
	}

	// An unset secret is redacted too: the log does not say whether it is set.
	c, err = load(nil, vars)
	if err != nil {
		t.Fatal(err)
	}
	if v := c.logAttrs()[0].Value.String(); v != redacted {
		t.Errorf("unset secret shown as %q", v)
	}

	// The value of an unknown variable never reaches the error: it may be a
	// misspelled secret.
	_, err = loadConfig([]string{"RAUN_SECRET_KEYY=key-sentinel"})
	if err == nil || strings.Contains(err.Error(), "key-sentinel") {
		t.Errorf("error = %v", err)
	}
}
