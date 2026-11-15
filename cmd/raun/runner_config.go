package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kvitrvn/raun/internal/core/job"
)

// Bounds of RAUN_RUNNER_CAPACITY.
const maxRunnerCapacity = 64

// runnerConfig is the configuration of raun runner, read from the
// RAUN_RUNNER_* variables only; the server's are ignored, so one
// environment can hold both.
type runnerConfig struct {
	server   string
	token    job.Secret
	capacity int
	workDir  string
	settings []setting
}

// runnerVariable is one RAUN_RUNNER_* variable: its default, whether it is
// required, and the rule that reads it.
type runnerVariable struct {
	name     string
	def      string
	required bool
	secret   bool
	parse    func(c *runnerConfig, value string) error
}

// runnerVariables is the catalogue of the runner. A RAUN_RUNNER_* variable
// outside it stops the runner.
var runnerVariables = []runnerVariable{
	{name: "RAUN_RUNNER_SERVER", required: true, parse: parseRunnerServer},
	{name: "RAUN_RUNNER_TOKEN", required: true, secret: true, parse: parseRunnerToken},
	{name: "RAUN_RUNNER_CAPACITY", def: "1", parse: parseRunnerCapacity},
	{name: "RAUN_RUNNER_WORKDIR", def: defaultWorkDir(), parse: parseRunnerWorkDir},
}

// workDirLock is the file of the work directory a runner holds the lock of.
const workDirLock = ".lock"

// defaultWorkDir is the work directory of a runner when none is set:
// raun-runner-<uid> in the temporary directory, one per user, raun-runner
// where the system has no user identifier.
func defaultWorkDir() string {
	if uid := os.Getuid(); uid >= 0 {
		return filepath.Join(os.TempDir(), "raun-runner-"+strconv.Itoa(uid))
	}
	return filepath.Join(os.TempDir(), "raun-runner")
}

// loadRunnerConfig reads the configuration of the runner from environ, in
// the form of os.Environ. Every error names its variable, never a secret;
// they are all returned, in the order of their text.
func loadRunnerConfig(environ []string) (runnerConfig, error) {
	env := map[string]string{}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := env[name]; !seen {
			env[name] = value
		}
	}
	var errs []error
	known := map[string]bool{}
	for _, v := range runnerVariables {
		known[v.name] = true
		if v.secret {
			known[v.name+fileSuffix] = true
		}
	}
	for name := range env {
		if strings.HasPrefix(name, runnerPrefix) && !known[name] {
			errs = append(errs, fmt.Errorf("%s: unknown variable", name))
		}
	}
	var c runnerConfig
	for _, v := range runnerVariables {
		value, ok, err := lookup(env, variable{name: v.name, secret: v.secret})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			if v.required {
				form := v.name
				if v.secret {
					form += " or " + v.name + fileSuffix
				}
				errs = append(errs, fmt.Errorf("%s: required", form))
				continue
			}
			value = v.def
		}
		shown := value
		if v.secret {
			shown = redacted
		}
		c.settings = append(c.settings, setting{name: v.name, value: shown, secret: v.secret})
		if err := v.parse(&c, value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", v.name, err))
		}
	}
	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return runnerConfig{}, errors.Join(errs...)
	}
	return c, nil
}

// parseRunnerServer reads the public address of the server, as
// RAUN_PUBLIC_URL is written: https, or http on a loopback address, so the
// token never travels in clear.
func parseRunnerServer(c *runnerConfig, value string) error {
	var server config
	if err := parsePublicURL(&server, value); err != nil {
		return err
	}
	c.server = server.publicURL.String()
	return nil
}

func parseRunnerToken(c *runnerConfig, value string) error {
	switch {
	case value == "":
		return errors.New("empty")
	case len(value) > job.MaxToken || strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return errors.New("not the token of a runner")
	}
	c.token = job.Secret(value)
	return nil
}

func parseRunnerCapacity(c *runnerConfig, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maxRunnerCapacity {
		return fmt.Errorf("%q is not a number of jobs from 1 to %d", value, maxRunnerCapacity)
	}
	c.capacity = n
	return nil
}

func parseRunnerWorkDir(c *runnerConfig, value string) error {
	if value == "" {
		return errors.New("empty")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	c.workDir = abs
	return nil
}

// logAttrs returns the effective configuration, the token as [redacted].
func (c runnerConfig) logAttrs() []slog.Attr {
	attrs := make([]slog.Attr, 0, len(c.settings))
	for _, s := range c.settings {
		attrs = append(attrs, slog.String(s.name, s.value))
	}
	return attrs
}

// runnerRelease is the release a runner says it runs, from the build: the
// module version when it is one job.ValidRelease accepts, dev otherwise.
func runnerRelease(bi buildInfo) string {
	if bi.version != "" && bi.version != "(devel)" && job.ValidRelease(bi.version) {
		return bi.version
	}
	return "dev"
}
