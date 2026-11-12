package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kvitrvn/raun/internal/adapter/driven/sqlite"
	"github.com/kvitrvn/raun/internal/adapter/driving/web"
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

	// settings are the effective values, in the order of the variables,
	// for the startup log.
	settings []setting
}

// setting is the effective value of one variable.
type setting struct {
	name   string
	value  string
	secret bool
}

// variable is one RAUN_* variable of the server: its default and the rule
// that reads it into a config.
type variable struct {
	name   string
	def    string
	secret bool
	parse  func(c *config, value string) error
}

// runnerPrefix starts the variables of the runner, which the server ignores.
const runnerPrefix = "RAUN_RUNNER_"

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
	}
	for name := range env {
		if strings.HasPrefix(name, "RAUN_") && !strings.HasPrefix(name, runnerPrefix) && !known[name] {
			// The value is never shown: a misspelled secret is still a secret.
			errs = append(errs, fmt.Errorf("%s: unknown variable", name))
		}
	}

	var c config
	for _, v := range vars {
		value, ok := env[v.name]
		if !ok {
			value = v.def
		}
		c.settings = append(c.settings, setting{name: v.name, value: value, secret: v.secret})
		if err := v.parse(&c, value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.name, err))
		}
	}
	if err := c.loadCertificates(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return config{}, errors.Join(errs...)
	}
	return c, nil
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
