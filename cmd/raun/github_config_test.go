package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAppKey writes the PEM private key of a test App into dir and returns
// its file and its content.
func writeAppKey(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	file := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return file, string(data)
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// secretKey is a RAUN_SECRET_KEY of the tests, 32 bytes of n in base64.
func secretKey(n byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32))
}

// app is a complete GitHub App, the private key by file, with the secret key
// that seals the tokens it gives.
func app(keyFile string) []string {
	return []string{
		"RAUN_GITHUB_APP_ID=42",
		"RAUN_GITHUB_CLIENT_ID=Iv23liClientID",
		"RAUN_GITHUB_CLIENT_SECRET=client-secret-sentinel",
		"RAUN_GITHUB_PRIVATE_KEY_FILE=" + keyFile,
		"RAUN_GITHUB_WEBHOOK_SECRET=webhook-secret-sentinel",
		"RAUN_SECRET_KEY=" + secretKey(1),
	}
}

func TestLoadConfigGitHub(t *testing.T) {
	dir := t.TempDir()
	keyFile, keyPEM := writeAppKey(t, dir)
	secretFile := writeFile(t, dir, "secret", "secret-from-file\n")

	c, err := loadConfig(app(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	g := c.github
	if g.webURL != "https://github.com" || g.appID != 42 || g.clientID != "Iv23liClientID" ||
		g.clientSecret.Reveal() != "client-secret-sentinel" || g.privateKey == nil || g.webhookSecret.Reveal() != "webhook-secret-sentinel" {
		t.Errorf("github = %+v", g)
	}

	// The key in the variable itself, the secret by file, on GHES.
	c, err = loadConfig([]string{
		"RAUN_GITHUB_URL=https://ghe.example.com/", "RAUN_GITHUB_APP_ID=42", "RAUN_GITHUB_CLIENT_ID=Iv1.abc",
		"RAUN_GITHUB_CLIENT_SECRET_FILE=" + secretFile, "RAUN_GITHUB_PRIVATE_KEY=" + keyPEM,
		"RAUN_GITHUB_WEBHOOK_SECRET_FILE=" + secretFile,
		"RAUN_SECRET_KEY=" + secretKey(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.github.webURL != "https://ghe.example.com" || c.github.clientSecret.Reveal() != "secret-from-file" || c.github.privateKey == nil || c.github.webhookSecret.Reveal() != "secret-from-file" {
		t.Errorf("github = %+v", c.github)
	}
	// A file written on Windows loses its CRLF.
	crlf := writeFile(t, dir, "crlf", "secret-crlf\r\n")
	c, err = loadConfig(append(app(keyFile)[:2], "RAUN_GITHUB_CLIENT_SECRET_FILE="+crlf, "RAUN_GITHUB_PRIVATE_KEY_FILE="+keyFile, "RAUN_GITHUB_WEBHOOK_SECRET=w", "RAUN_SECRET_KEY="+secretKey(1)))
	if err != nil || c.github.clientSecret.Reveal() != "secret-crlf" {
		t.Errorf("secret from a CRLF file = %q, %v", c.github.clientSecret.Reveal(), err)
	}
}

func TestLoadConfigRefusesGitHub(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	big := writeFile(t, dir, "big", strings.Repeat("x", maxSecretFile+1))
	ec := writeFile(t, dir, "ec.pem", "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIBkey-sentinel\n-----END EC PRIVATE KEY-----\n")
	garbage := writeFile(t, dir, "garbage", "key-sentinel")
	without := func(name string) []string {
		var out []string
		for _, kv := range app(keyFile) {
			if !strings.HasPrefix(kv, name+"=") {
				out = append(out, kv)
			}
		}
		return out
	}
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"an App without its ID", without("RAUN_GITHUB_APP_ID"), "RAUN_GITHUB_APP_ID: unset, but RAUN_GITHUB_CLIENT_ID, RAUN_GITHUB_CLIENT_SECRET, RAUN_GITHUB_PRIVATE_KEY, RAUN_GITHUB_WEBHOOK_SECRET set"},
		{"a webhook secret alone", []string{"RAUN_GITHUB_WEBHOOK_SECRET=w"}, "RAUN_GITHUB_APP_ID: unset, but RAUN_GITHUB_WEBHOOK_SECRET set"},
		{"an App without its webhook secret", without("RAUN_GITHUB_WEBHOOK_SECRET"), "RAUN_GITHUB_APP_ID: set, so RAUN_GITHUB_WEBHOOK_SECRET must be set too"},
		{"a webhook secret with a space", append(without("RAUN_GITHUB_WEBHOOK_SECRET"), "RAUN_GITHUB_WEBHOOK_SECRET=webhook sentinel"), "RAUN_GITHUB_WEBHOOK_SECRET: not a webhook secret"},
		{"both forms of the webhook secret", append(app(keyFile), "RAUN_GITHUB_WEBHOOK_SECRET_FILE="+garbage), "RAUN_GITHUB_WEBHOOK_SECRET and RAUN_GITHUB_WEBHOOK_SECRET_FILE: set one or neither"},
		{"an address alone", []string{"RAUN_GITHUB_URL=https://ghe.example.com"}, "RAUN_GITHUB_APP_ID: unset, but RAUN_GITHUB_URL set"},
		{"an empty client ID alone", []string{"RAUN_GITHUB_CLIENT_ID="}, "RAUN_GITHUB_APP_ID: unset, but RAUN_GITHUB_CLIENT_ID set"},
		{"an App without its client ID", without("RAUN_GITHUB_CLIENT_ID"), "RAUN_GITHUB_APP_ID: set, so RAUN_GITHUB_CLIENT_ID must be set too"},
		{"an App without its secret", without("RAUN_GITHUB_CLIENT_SECRET"), "RAUN_GITHUB_APP_ID: set, so RAUN_GITHUB_CLIENT_SECRET must be set too"},
		{"an App without its key", without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_APP_ID: set, so RAUN_GITHUB_PRIVATE_KEY must be set too"},
		{"an ID alone", []string{"RAUN_GITHUB_APP_ID=42"}, "RAUN_GITHUB_CLIENT_ID, RAUN_GITHUB_CLIENT_SECRET, RAUN_GITHUB_PRIVATE_KEY, RAUN_GITHUB_WEBHOOK_SECRET must be set too"},
		{"an ID of zero", append(without("RAUN_GITHUB_APP_ID"), "RAUN_GITHUB_APP_ID=0"), "RAUN_GITHUB_APP_ID: \"0\" is not the number of a GitHub App"},
		{"an ID with a leading zero", append(without("RAUN_GITHUB_APP_ID"), "RAUN_GITHUB_APP_ID=042"), "RAUN_GITHUB_APP_ID:"},
		{"an ID that is a name", append(without("RAUN_GITHUB_APP_ID"), "RAUN_GITHUB_APP_ID=raun"), "RAUN_GITHUB_APP_ID:"},
		{"an http address", append(app(keyFile), "RAUN_GITHUB_URL=http://ghe.example.com"), "RAUN_GITHUB_URL: \"http://ghe.example.com\" is not an https URL"},
		{"an address with a path", append(app(keyFile), "RAUN_GITHUB_URL=https://ghe.example.com/github"), "RAUN_GITHUB_URL:"},
		{"a client ID with a space", append(without("RAUN_GITHUB_CLIENT_ID"), "RAUN_GITHUB_CLIENT_ID=Iv1 x"), "RAUN_GITHUB_CLIENT_ID:"},
		{"a secret with a space", append(without("RAUN_GITHUB_CLIENT_SECRET"), "RAUN_GITHUB_CLIENT_SECRET=secret sentinel"), "RAUN_GITHUB_CLIENT_SECRET: not a client secret"},
		{"both forms of a secret", append(app(keyFile), "RAUN_GITHUB_PRIVATE_KEY=x"), "RAUN_GITHUB_PRIVATE_KEY and RAUN_GITHUB_PRIVATE_KEY_FILE: set one or neither"},
		{"both forms, one empty", append(app(keyFile), "RAUN_GITHUB_CLIENT_SECRET_FILE="), "RAUN_GITHUB_CLIENT_SECRET and RAUN_GITHUB_CLIENT_SECRET_FILE: set one or neither"},
		{"a missing file", append(without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_PRIVATE_KEY_FILE="+filepath.Join(dir, "none")), "RAUN_GITHUB_PRIVATE_KEY_FILE: open"},
		{"an empty file path", append(without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_PRIVATE_KEY_FILE="), "RAUN_GITHUB_PRIVATE_KEY_FILE: empty path"},
		{"an oversized file", append(without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_PRIVATE_KEY_FILE="+big), "RAUN_GITHUB_PRIVATE_KEY_FILE: " + big + " is larger than"},
		{"a key that is not RSA", append(without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_PRIVATE_KEY_FILE="+ec), "RAUN_GITHUB_PRIVATE_KEY: not the private key of a GitHub App"},
		{"a key that is not PEM", append(without("RAUN_GITHUB_PRIVATE_KEY_FILE"), "RAUN_GITHUB_PRIVATE_KEY_FILE="+garbage), "RAUN_GITHUB_PRIVATE_KEY: not the private key of a GitHub App"},
		{"the _FILE form of a variable that is not secret", []string{"RAUN_GITHUB_CLIENT_ID_FILE=/x"}, "RAUN_GITHUB_CLIENT_ID_FILE: unknown variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfig(tt.env)
			if err == nil {
				t.Fatal("loadConfig accepted the configuration")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("error %q holds a secret", err)
			}
		})
	}
}

// TestGitHubSecretsAreNeverShown: no format, log or error of the
// configuration shows the client secret or the private key.
func TestGitHubSecretsAreNeverShown(t *testing.T) {
	dir := t.TempDir()
	keyFile, keyPEM := writeAppKey(t, dir)
	c, err := loadConfig(app(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	keyLine := strings.Split(keyPEM, "\n")[1]
	var outputs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		outputs = append(outputs, fmt.Sprintf(verb, c.github.clientSecret), fmt.Sprintf(verb, c.settings))
	}
	for _, s := range c.settings {
		outputs = append(outputs, s.value)
	}
	b, err := json.Marshal(struct{ Secret any }{c.github.clientSecret})
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(b))
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).LogAttrs(t.Context(), slog.LevelInfo, "configuration", c.logAttrs()...)
	outputs = append(outputs, logs.String())
	for _, out := range outputs {
		if strings.Contains(out, "client-secret-sentinel") || strings.Contains(out, keyLine) {
			t.Errorf("a secret shows in %q", out)
		}
	}
	if !strings.Contains(logs.String(), `"RAUN_GITHUB_PRIVATE_KEY":"[redacted]"`) {
		t.Errorf("the key is not redacted in %s", logs.String())
	}
}

func TestNewForge(t *testing.T) {
	dir := t.TempDir()
	keyFile, _ := writeAppKey(t, dir)
	c, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := newForge(c); f != nil || err != nil {
		t.Errorf("newForge without an App = %v, %v", f, err)
	}
	c, err = loadConfig(append(app(keyFile), "RAUN_PUBLIC_URL=https://raun.example"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := newForge(c)
	if err != nil || f == nil || f.Name() != "github" {
		t.Fatalf("newForge = %v, %v", f, err)
	}
	if u := f.AuthorizeURL("s", "c"); !strings.HasPrefix(u, "https://github.com/login/oauth/authorize?") ||
		!strings.Contains(u, "redirect_uri=https%3A%2F%2Fraun.example%2Flogin%2Fcallback") {
		t.Errorf("AuthorizeURL = %s", u)
	}
}
