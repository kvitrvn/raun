// Package config loads and validates the Raun project configuration
// (`.raun/config.yaml`).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kvitrvn/raun/internal/source"
)

// CurrentVersion is the only configuration format version this build reads.
const CurrentVersion = 1

// Defaults applied when a field is omitted.
const (
	DefaultTimeout      = 20 * time.Minute
	DefaultQuorum       = 2
	DefaultMaxLines     = 40
	DefaultLanguage     = "en"
	DefaultRunnerKind   = RunnerCommand
	BuiltinAnalyst      = "builtin:analyst"
	BuiltinLead         = "builtin:lead"
	BuiltinReconciler   = "builtin:reconciler"
	builtinInstructions = "builtin:"
)

// MaxAgents is the largest number of analysts; the lead sees them
// anonymized as A to Z.
const MaxAgents = 26

// reservedIDs name roles, which share the run artifact layout with agents.
var reservedIDs = []string{"lead", "reconciler"}

// RunnerCommand runs an agent as an external command: prompt on stdin,
// one JSON object on stdout.
const RunnerCommand = "command"

// KnownTypes lists the knowledge types built into Raun.
var KnownTypes = []string{"persona", "requirement"}

var (
	agentIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	languagePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// Config is the parsed content of `.raun/config.yaml`.
type Config struct {
	Version int `yaml:"version"`
	// Language is the language agents write knowledge in (BCP 47, e.g.
	// "en", "fr"). Quoted excerpts always keep their original language.
	Language   string   `yaml:"language"`
	Sources    Sources  `yaml:"sources"`
	Types      []string `yaml:"types"`
	Evidence   Evidence `yaml:"evidence"`
	Analysis   Analysis `yaml:"analysis"`
	Lead       *Role    `yaml:"lead,omitempty"`
	Reconciler *Role    `yaml:"reconciler,omitempty"`
}

// Evidence configures evidence verification.
type Evidence struct {
	// MaxLines is the longest line range a single evidence may cite.
	MaxLines int `yaml:"max_lines"`
}

// Sources selects which repository files agents may cite.
type Sources struct {
	// Exclude holds glob patterns of repository paths that are not sources.
	Exclude []string `yaml:"exclude"`
	// Docs holds glob patterns of paths classified as documentation; other
	// paths are code. Nil means source.DefaultDocs.
	Docs []string `yaml:"docs"`
	// Context lists repository paths written by the team; they are sources
	// of nature "human-context".
	Context []string `yaml:"context"`
}

// Analysis configures the independent analysis phase.
type Analysis struct {
	// Quorum is the minimum number of valid agent reports for a run to proceed.
	Quorum int     `yaml:"quorum"`
	Agents []Agent `yaml:"agents"`
}

// Agent is one independent analyst.
type Agent struct {
	ID           string `yaml:"id"`
	Instructions string `yaml:"instructions"`
	Runner       Runner `yaml:"runner"`
}

// Role configures a single-instance role such as the lead or the reconciler.
type Role struct {
	Instructions string `yaml:"instructions"`
	Runner       Runner `yaml:"runner"`
}

// Runner describes how an agent is executed.
type Runner struct {
	Kind    string   `yaml:"kind"`
	Argv    []string `yaml:"argv"`
	Timeout Duration `yaml:"timeout"`
}

// Duration is a time.Duration written as a Go duration string ("20m").
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"20m\"", node.Line)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", node.Line, s)
	}
	*d = Duration(v)
	return nil
}

// Load reads, decodes, defaults and validates the configuration file at p.
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg, err := Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return cfg, nil
}

// Parse decodes, defaults and validates a configuration. Unknown fields are
// rejected so that typos do not silently fall back to defaults.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
	if c.Sources.Docs == nil {
		c.Sources.Docs = slices.Clone(source.DefaultDocs)
	}
	if c.Evidence.MaxLines == 0 {
		c.Evidence.MaxLines = DefaultMaxLines
	}
	if c.Analysis.Quorum == 0 {
		c.Analysis.Quorum = min(DefaultQuorum, len(c.Analysis.Agents))
	}
	for i := range c.Analysis.Agents {
		a := &c.Analysis.Agents[i]
		if a.Instructions == "" {
			a.Instructions = BuiltinAnalyst
		}
		a.Runner.applyDefaults()
	}
	if c.Lead != nil {
		c.Lead.applyDefaults(BuiltinLead)
	}
	if c.Reconciler != nil {
		c.Reconciler.applyDefaults(BuiltinReconciler)
	}
}

func (r *Role) applyDefaults(instructions string) {
	if r.Instructions == "" {
		r.Instructions = instructions
	}
	r.Runner.applyDefaults()
}

func (r *Runner) applyDefaults() {
	if r.Kind == "" {
		r.Kind = DefaultRunnerKind
	}
	if r.Timeout == 0 {
		r.Timeout = Duration(DefaultTimeout)
	}
}

// FieldError reports an invalid value at a dotted field path.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Msg
}

// Validate checks semantic constraints. All problems are reported at once,
// joined, each as a *FieldError.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}

	if c.Version != CurrentVersion {
		add("version", "must be %d, got %d", CurrentVersion, c.Version)
	}
	if c.Evidence.MaxLines < 1 {
		add("evidence.max_lines", "must be at least 1, got %d", c.Evidence.MaxLines)
	}

	if !languagePattern.MatchString(c.Language) {
		add("language", "must be a language tag such as \"en\" or \"fr\", got %q", c.Language)
	}
	for i, p := range c.Sources.Exclude {
		if err := source.ValidPattern(p); err != nil {
			add(fmt.Sprintf("sources.exclude[%d]", i), "%v", err)
		}
	}
	for i, p := range c.Sources.Docs {
		if err := source.ValidPattern(p); err != nil {
			add(fmt.Sprintf("sources.docs[%d]", i), "%v", err)
		}
	}
	for i, p := range c.Sources.Context {
		if msg := checkRepoPath(p); msg != "" {
			add(fmt.Sprintf("sources.context[%d]", i), "%s", msg)
		}
	}

	if len(c.Types) == 0 {
		add("types", "must list at least one knowledge type (known: %s)", strings.Join(KnownTypes, ", "))
	}
	seenTypes := map[string]bool{}
	for i, t := range c.Types {
		field := fmt.Sprintf("types[%d]", i)
		switch {
		case !slices.Contains(KnownTypes, t):
			add(field, "unknown knowledge type %q (known: %s)", t, strings.Join(KnownTypes, ", "))
		case seenTypes[t]:
			add(field, "duplicate knowledge type %q", t)
		}
		seenTypes[t] = true
	}

	agents := c.Analysis.Agents
	switch {
	case len(agents) == 0:
		add("analysis.agents", "must declare at least one agent")
	case len(agents) > MaxAgents:
		add("analysis.agents", "must declare at most %d agents, got %d", MaxAgents, len(agents))
	}
	if c.Analysis.Quorum < 1 || c.Analysis.Quorum > len(agents) {
		add("analysis.quorum", "must be between 1 and the number of agents (%d), got %d", len(agents), c.Analysis.Quorum)
	}
	seenIDs := map[string]bool{}
	for i, a := range agents {
		field := fmt.Sprintf("analysis.agents[%d]", i)
		switch {
		case a.ID == "":
			add(field+".id", "must not be empty")
		case !agentIDPattern.MatchString(a.ID):
			add(field+".id", "must match %s, got %q", agentIDPattern, a.ID)
		case seenIDs[a.ID]:
			add(field+".id", "duplicate agent id %q", a.ID)
		case slices.Contains(reservedIDs, a.ID):
			add(field+".id", "%q is reserved", a.ID)
		}
		seenIDs[a.ID] = true
		errs = append(errs, validateRole(field, Role{Instructions: a.Instructions, Runner: a.Runner})...)
	}

	if c.Lead != nil {
		errs = append(errs, validateRole("lead", *c.Lead)...)
	}
	if c.Reconciler != nil {
		errs = append(errs, validateRole("reconciler", *c.Reconciler)...)
	}

	return errors.Join(errs...)
}

func validateRole(field string, r Role) []error {
	var errs []error
	add := func(f, format string, args ...any) {
		errs = append(errs, &FieldError{Field: field + "." + f, Msg: fmt.Sprintf(format, args...)})
	}

	if name, ok := strings.CutPrefix(r.Instructions, builtinInstructions); ok {
		if name == "" {
			add("instructions", "builtin instructions need a name, e.g. %q", BuiltinAnalyst)
		}
	} else if msg := checkRepoPath(r.Instructions); msg != "" {
		add("instructions", "%s", msg)
	}

	if r.Runner.Kind != RunnerCommand {
		add("runner.kind", "unsupported runner kind %q (supported: %s)", r.Runner.Kind, RunnerCommand)
	}
	if len(r.Runner.Argv) == 0 || r.Runner.Argv[0] == "" {
		add("runner.argv", "must contain at least the command to run")
	}
	if r.Runner.Timeout < 0 {
		add("runner.timeout", "must not be negative")
	}
	return errs
}

// checkRepoPath returns a problem description if p is not a relative path
// staying inside the repository, or "" if it is acceptable.
func checkRepoPath(p string) string {
	switch {
	case p == "":
		return "must not be empty"
	case path.IsAbs(p) || strings.HasPrefix(p, `\`):
		return fmt.Sprintf("must be relative to the repository root, got %q", p)
	case path.Clean(p) == ".." || strings.HasPrefix(path.Clean(p), "../"):
		return fmt.Sprintf("must stay inside the repository, got %q", p)
	}
	return ""
}
