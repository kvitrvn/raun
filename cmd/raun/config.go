package main

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/github"
	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/adapter/driving/web"
	"github.com/kvitrvn/raun/internal/core/identity"
)

// redacted replaces the value of a secret wherever a setting is shown.
const redacted = "[redacted]"

// Bounds of RAUN_SHUTDOWN_TIMEOUT.
const (
	minShutdownTimeout = time.Second
	maxShutdownTimeout = 5 * time.Minute
)

// config is the configuration of raun server, read from RAUN_* variables
// only (docs/implementation/03-server-foundation.md#configuration).
type config struct {
	httpAddr        string
	publicURL       *url.URL
	publicHost      string // host of publicURL, as web.CanonicalHost gives it
	tlsCertFile     string
	tlsKeyFile      string
	certificates    *web.Certificates // loaded when both TLS files are set
	databasePath    string
	databaseMigrate bool
	logLevel        slog.Level
	logFormat       string
	shutdownTimeout time.Duration
	github          githubConfig
	signup          identity.Policy
	// secretKey and previousKey seal the secrets the server stores, 32
	// bytes each; nil when unset.
	secretKey   []byte
	previousKey []byte

	// settings are the effective values, in the order of the variables,
	// for the startup log.
	settings []setting
}

// githubConfig is the GitHub App of the server. A zero appID means no App:
// the server has no forge.
type githubConfig struct {
	webURL       string
	appID        int64
	clientID     string
	clientSecret identity.Secret
	privateKey   *rsa.PrivateKey
}

// setting is the effective value of one variable.
type setting struct {
	name   string
	value  string
	secret bool
}

// variable is one RAUN_* variable of the server: its default and the rule
// that reads it into a config. A secret can also be given as a file, named by
// the variable with fileSuffix.
type variable struct {
	name   string
	def    string
	secret bool
	parse  func(c *config, value string) error
}

// runnerPrefix starts the variables of the runner, which the server ignores.
const runnerPrefix = "RAUN_RUNNER_"

// fileSuffix names the variable that gives a secret as a file:
// RAUN_GITHUB_PRIVATE_KEY_FILE for RAUN_GITHUB_PRIVATE_KEY.
const fileSuffix = "_FILE"

// maxSecretFile bounds the file of a secret: a private key of 4096 bits
// takes about 3 KiB.
const maxSecretFile = 64 << 10

// variables is the catalogue of the server. A RAUN_* variable outside it,
// and outside the runner's prefix, stops the server.
var variables = []variable{
	{name: "RAUN_HTTP_ADDR", def: ":8080", parse: parseHTTPAddr},
	{name: "RAUN_PUBLIC_URL", def: "http://localhost:8080", parse: parsePublicURL},
	{name: "RAUN_TLS_CERT_FILE", def: "", parse: func(c *config, v string) error { c.tlsCertFile = v; return nil }},
	{name: "RAUN_TLS_KEY_FILE", def: "", parse: func(c *config, v string) error { c.tlsKeyFile = v; return nil }},
	{name: "RAUN_DATABASE_PATH", def: "raun.db", parse: parseDatabasePath},
	{name: "RAUN_DATABASE_MIGRATE", def: "false", parse: parseDatabaseMigrate},
	{name: "RAUN_LOG_LEVEL", def: "info", parse: parseLogLevel},
	{name: "RAUN_LOG_FORMAT", def: "json", parse: parseLogFormat},
	{name: "RAUN_SHUTDOWN_TIMEOUT", def: "10s", parse: parseShutdownTimeout},
	{name: "RAUN_GITHUB_URL", def: github.DefaultWebURL, parse: parseGitHubURL},
	{name: "RAUN_GITHUB_APP_ID", def: "", parse: parseGitHubAppID},
	{name: "RAUN_GITHUB_CLIENT_ID", def: "", parse: parseGitHubClientID},
	{name: "RAUN_GITHUB_CLIENT_SECRET", def: "", secret: true, parse: parseGitHubClientSecret},
	{name: "RAUN_GITHUB_PRIVATE_KEY", def: "", secret: true, parse: parseGitHubPrivateKey},
	{name: "RAUN_SECRET_KEY", def: "", secret: true, parse: func(c *config, v string) error { return parseSecretKey(&c.secretKey, v) }},
	{name: "RAUN_SECRET_KEY_PREVIOUS", def: "", secret: true, parse: func(c *config, v string) error { return parseSecretKey(&c.previousKey, v) }},
	{name: "RAUN_SIGNUP_USERS", def: "", parse: func(c *config, v string) error { return parseEntries(&c.signup.Users, v) }},
	{name: "RAUN_SIGNUP_ORGS", def: "", parse: func(c *config, v string) error { return parseEntries(&c.signup.Orgs, v) }},
	{name: "RAUN_ADMINS", def: "", parse: func(c *config, v string) error { return parseEntries(&c.signup.Admins, v) }},
}

// loadConfig reads the configuration from environ, in the form of
// os.Environ. Every error names its variable; they are all returned, in the
// order of their text.
func loadConfig(environ []string) (config, error) {
	return load(environ, variables)
}

func load(environ []string, vars []variable) (config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// The first value wins, as for os.Getenv.
		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}

	var errs []error
	known := map[string]bool{}
	for _, v := range vars {
		known[v.name] = true
		if v.secret {
			known[v.name+fileSuffix] = true
		}
	}
	for name := range env {
		if strings.HasPrefix(name, "RAUN_") && !strings.HasPrefix(name, runnerPrefix) && !known[name] {
			// The value is never shown: a misspelled secret is still a secret.
			errs = append(errs, fmt.Errorf("%s: unknown variable", name))
		}
	}

	var c config
	set := map[string]bool{}
	for _, v := range vars {
		value, ok, err := lookup(env, v)
		if err != nil {
			errs = append(errs, err)
		}
		set[v.name] = ok
		if !ok {
			value = v.def
		}
		shown := value
		if v.secret {
			shown = redacted
		}
		c.settings = append(c.settings, setting{name: v.name, value: shown, secret: v.secret})
		if err != nil {
			continue
		}
		if err := v.parse(&c, value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.name, err))
		}
	}
	if err := c.loadCertificates(); err != nil {
		errs = append(errs, err)
	}
	if err := c.checkGitHub(set); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.checkIdentities()...)

	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return config{}, errors.Join(errs...)
	}
	return c, nil
}

// lookup returns the value of v in env, and whether it is set. A secret is
// set by its variable or by the file its _FILE variable names, never both;
// one trailing newline of the file is dropped. An error names the variable
// and never holds the value.
func lookup(env map[string]string, v variable) (string, bool, error) {
	value, ok := env[v.name]
	if !v.secret {
		return value, ok, nil
	}
	path, fromFile := env[v.name+fileSuffix]
	switch {
	case ok && fromFile:
		return "", true, fmt.Errorf("%s and %s%s: set one or neither", v.name, v.name, fileSuffix)
	case !fromFile:
		return value, ok, nil
	}
	data, err := readSecretFile(path)
	if err != nil {
		return "", true, fmt.Errorf("%s%s: %w", v.name, fileSuffix, err)
	}
	return data, true, nil
}

// readSecretFile reads the secret in the file at path, up to maxSecretFile
// bytes, without its trailing newline.
func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxSecretFile {
		return "", fmt.Errorf("%s is larger than %d bytes", path, maxSecretFile)
	}
	s := strings.TrimSuffix(string(data), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

// logAttrs returns the effective configuration, each secret replaced by
// [redacted] whether set or not.
func (c config) logAttrs() []slog.Attr {
	attrs := make([]slog.Attr, 0, len(c.settings))
	for _, s := range c.settings {
		value := s.value
		if s.secret {
			value = redacted
		}
		attrs = append(attrs, slog.String(s.name, value))
	}
	return attrs
}

// checkGitHub checks that the GitHub App is set whole or not at all:
// RAUN_GITHUB_APP_ID decides, and the other variables of the App follow it.
func (c *config) checkGitHub(set map[string]bool) error {
	others := []string{"RAUN_GITHUB_URL", "RAUN_GITHUB_CLIENT_ID", "RAUN_GITHUB_CLIENT_SECRET", "RAUN_GITHUB_PRIVATE_KEY"}
	if c.github.appID == 0 {
		var given []string
		for _, name := range others {
			if set[name] {
				given = append(given, name)
			}
		}
		if len(given) > 0 {
			return fmt.Errorf("RAUN_GITHUB_APP_ID: unset, but %s set: configure the whole GitHub App or none of it", strings.Join(given, ", "))
		}
		return nil
	}
	var missing []string
	if c.github.clientID == "" {
		missing = append(missing, "RAUN_GITHUB_CLIENT_ID")
	}
	if c.github.clientSecret == "" {
		missing = append(missing, "RAUN_GITHUB_CLIENT_SECRET")
	}
	if c.github.privateKey == nil {
		missing = append(missing, "RAUN_GITHUB_PRIVATE_KEY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("RAUN_GITHUB_APP_ID: set, so %s must be set too", strings.Join(missing, ", "))
	}
	return nil
}

// forges are the names of the forges cfg configures.
func (c *config) forges() []string {
	if c.github.appID != 0 {
		return []string{github.Name}
	}
	return nil
}

// checkIdentities checks the rules between the identities and the keys: an
// entry names a configured forge, a forge needs RAUN_SECRET_KEY to store the
// tokens it gives, and the previous key goes with a current one and differs
// from it.
func (c *config) checkIdentities() []error {
	var errs []error
	forges := c.forges()
	for _, list := range []struct {
		name    string
		entries []identity.Entry
	}{{"RAUN_ADMINS", c.signup.Admins}, {"RAUN_SIGNUP_ORGS", c.signup.Orgs}, {"RAUN_SIGNUP_USERS", c.signup.Users}} {
		for _, e := range list.entries {
			if !slices.Contains(forges, e.Forge) {
				errs = append(errs, fmt.Errorf("%s: %s names the forge %q, which is not configured", list.name, e, e.Forge))
			}
		}
	}
	if len(forges) > 0 && c.secretKey == nil {
		errs = append(errs, errors.New("RAUN_SECRET_KEY: required with a forge, to store the tokens it gives encrypted"))
	}
	if c.previousKey != nil && c.secretKey == nil {
		errs = append(errs, errors.New("RAUN_SECRET_KEY_PREVIOUS: set without RAUN_SECRET_KEY"))
	}
	if c.previousKey != nil && c.secretKey != nil && string(c.previousKey) == string(c.secretKey) {
		errs = append(errs, errors.New("RAUN_SECRET_KEY_PREVIOUS: the same key as RAUN_SECRET_KEY"))
	}
	return errs
}

// loadCertificates checks that both TLS files are set or neither, and loads
// the pair, so that a broken pair stops the server at startup.
func (c *config) loadCertificates() error {
	switch {
	case c.tlsCertFile == "" && c.tlsKeyFile == "":
		return nil
	case c.tlsCertFile == "" || c.tlsKeyFile == "":
		return errors.New("RAUN_TLS_CERT_FILE and RAUN_TLS_KEY_FILE: set both or neither")
	}
	certs, err := web.LoadCertificates(c.tlsCertFile, c.tlsKeyFile)
	if err != nil {
		return fmt.Errorf("RAUN_TLS_CERT_FILE and RAUN_TLS_KEY_FILE: %w", err)
	}
	c.certificates = certs
	return nil
}

func parseHTTPAddr(c *config, value string) error {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%q is not host:port: %w", value, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("port %q is not a number from 1 to 65535", port)
	}
	c.httpAddr = value
	return nil
}

func parsePublicURL(c *config, value string) error {
	// A query or a fragment, even empty, has no place in the address.
	if strings.ContainsAny(value, "?#") {
		return fmt.Errorf("%q has a query or a fragment", value)
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", value, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q is not an http or https URL", value)
	}
	if u.Opaque != "" || u.User != nil {
		return fmt.Errorf("%q must hold a scheme, a host and an optional port only", value)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%q has a path: the server is served at the root of its host", value)
	}
	host, err := web.CanonicalHost(u.Host, u.Scheme)
	if err != nil {
		return fmt.Errorf("%q: %w", value, err)
	}
	if u.Scheme == "http" && !isLoopback(strings.TrimSuffix(u.Hostname(), ".")) {
		return fmt.Errorf("%q: http is allowed on a loopback address only; use https", value)
	}
	c.publicURL = &url.URL{Scheme: u.Scheme, Host: u.Host}
	c.publicHost = host
	return nil
}

// isLoopback reports whether host is localhost or a loopback IP address.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseDatabasePath(c *config, value string) error {
	if err := sqlite.CheckPath(value); err != nil {
		return err
	}
	c.databasePath = value
	return nil
}

func parseDatabaseMigrate(c *config, value string) error {
	switch value {
	case "true":
		c.databaseMigrate = true
	case "false":
		c.databaseMigrate = false
	default:
		return fmt.Errorf("%q is not true or false", value)
	}
	return nil
}

func parseLogLevel(c *config, value string) error {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[value]
	if !ok {
		return fmt.Errorf("%q is not debug, info, warn or error", value)
	}
	c.logLevel = level
	return nil
}

func parseLogFormat(c *config, value string) error {
	if value != "json" && value != "text" {
		return fmt.Errorf("%q is not json or text", value)
	}
	c.logFormat = value
	return nil
}

func parseShutdownTimeout(c *config, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 10s", value)
	}
	if d < minShutdownTimeout || d > maxShutdownTimeout {
		return fmt.Errorf("%s is not from %s to %s", d, minShutdownTimeout, maxShutdownTimeout)
	}
	c.shutdownTimeout = d
	return nil
}

func parseGitHubURL(c *config, value string) error {
	if err := github.CheckWebURL(value); err != nil {
		return err
	}
	c.github.webURL = strings.TrimSuffix(value, "/")
	return nil
}

func parseGitHubAppID(c *config, value string) error {
	if value == "" {
		return nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return fmt.Errorf("%q is not the number of a GitHub App", value)
	}
	c.github.appID = id
	return nil
}

func parseGitHubClientID(c *config, value string) error {
	if len(value) > 100 || strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) {
		return fmt.Errorf("%q is not a client ID: letters, digits, dots, underscores and hyphens, 100 at most", value)
	}
	c.github.clientID = value
	return nil
}

func parseGitHubClientSecret(c *config, value string) error {
	// The value is never shown, even when it is refused.
	if len(value) > 256 || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return errors.New("not a client secret: printable characters without spaces, 256 at most")
	}
	c.github.clientSecret = identity.Secret(value)
	return nil
}

func parseGitHubPrivateKey(c *config, value string) error {
	if value == "" {
		return nil
	}
	key, err := github.ParsePrivateKey([]byte(value))
	if err != nil {
		return fmt.Errorf("not the private key of a GitHub App: %w", err)
	}
	c.github.privateKey = key
	return nil
}

func parseSecretKey(dst *[]byte, value string) error {
	if value == "" {
		return nil
	}
	// The value is never shown, even when it is refused.
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != sqlite.KeySize {
		return fmt.Errorf("not %d bytes in base64: generate one with openssl rand -base64 %d", sqlite.KeySize, sqlite.KeySize)
	}
	*dst = key
	return nil
}

func parseEntries(dst *[]identity.Entry, value string) error {
	entries, err := identity.ParseEntries(value)
	if err != nil {
		return err
	}
	*dst = entries
	return nil
}
