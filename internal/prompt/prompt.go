// Package prompt renders the prompts Raun sends to agents. Templates are
// embedded; the output contract is always appended by Raun, so custom
// instructions cannot change it.
package prompt

import (
	"embed"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/kvitrvn/raun/internal/knowledge"
)

//go:embed templates
var templates embed.FS

var (
	task      = template.Must(template.ParseFS(templates, "templates/task.md.tmpl"))
	lead      = template.Must(template.ParseFS(templates, "templates/lead.md.tmpl"))
	reconcile = template.Must(template.ParseFS(templates, "templates/reconcile.md.tmpl"))
	retry     = template.Must(template.ParseFS(templates, "templates/retry.md.tmpl"))
)

// RetryHeading starts every retry prompt.
const RetryHeading = "# Raun report retry"

// MaxRetryOutput bounds how much of an agent's previous output a retry
// prompt shows: its end, where an answer would be.
const MaxRetryOutput = 64 << 10

// Builtin returns the built-in instructions with the given name
// (the part after "builtin:" in the configuration).
func Builtin(name string) (string, error) {
	data, err := templates.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("unknown builtin instructions %q", name)
	}
	return string(data), nil
}

// Analyst holds what an analyst prompt is built from.
type Analyst struct {
	// Instructions describe the agent's job: built-in or custom.
	Instructions string
	Commit       string
	Language     string
	Types        []knowledge.Type
	ContextFiles []string
	Exclude      []string
	MaxLines     int
}

// Render returns the full analyst prompt: instructions, project facts,
// knowledge types and the output contract.
func (a Analyst) Render() (string, error) {
	var b strings.Builder
	err := task.Execute(&b, struct {
		Analyst
		HasPersona, HasRequirement bool
	}{
		Analyst:        a,
		HasPersona:     slices.Contains(a.Types, knowledge.TypePersona),
		HasRequirement: slices.Contains(a.Types, knowledge.TypeRequirement),
	})
	if err != nil {
		return "", fmt.Errorf("render analyst prompt: %w", err)
	}
	return b.String(), nil
}

// Lead holds what a lead prompt is built from.
type Lead struct {
	Instructions string
	Commit       string
	Language     string
	Types        []knowledge.Type
	// Reports is the anonymized JSON rendering of the analyst reports.
	Reports string
}

// Render returns the full lead prompt: instructions, project facts,
// knowledge types, the anonymized reports and the output contract.
func (l Lead) Render() (string, error) {
	var b strings.Builder
	data := struct {
		Lead
		HasPersona, HasRequirement bool
		ExampleType                knowledge.Type
	}{
		Lead:           l,
		HasPersona:     slices.Contains(l.Types, knowledge.TypePersona),
		HasRequirement: slices.Contains(l.Types, knowledge.TypeRequirement),
	}
	if len(l.Types) > 0 {
		data.ExampleType = l.Types[0]
	}
	if err := lead.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render lead prompt: %w", err)
	}
	return b.String(), nil
}

// Reconciler holds what a reconciler prompt is built from.
type Reconciler struct {
	Instructions string
	Commit       string
	Language     string
	Types        []knowledge.Type
	// Items is the JSON rendering of the existing items and of the run's
	// items.
	Items string
}

// Render returns the full reconciler prompt: instructions, project facts,
// knowledge types, the items to match and the output contract.
func (r Reconciler) Render() (string, error) {
	var b strings.Builder
	data := struct {
		Reconciler
		HasPersona, HasRequirement bool
		ExampleType                knowledge.Type
	}{
		Reconciler:     r,
		HasPersona:     slices.Contains(r.Types, knowledge.TypePersona),
		HasRequirement: slices.Contains(r.Types, knowledge.TypeRequirement),
	}
	if len(r.Types) > 0 {
		data.ExampleType = r.Types[0]
	}
	if err := reconcile.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render reconciler prompt: %w", err)
	}
	return b.String(), nil
}

// Retry holds what a retry prompt is built from: the prompt an agent was
// given, what was wrong with its answer, and what it printed.
type Retry struct {
	// Task is the full prompt of the first attempt. The retry keeps it from
	// its "## Project" section on: facts, types, input and output contract.
	Task string
	// Problems explains why the output was rejected, one line per problem.
	Problems string
	// Output is the agent's previous stdout; only its end is shown.
	Output []byte
}

// Render returns the retry prompt.
func (r Retry) Render() (string, error) {
	task := r.Task
	if i := strings.Index(task, "\n## Project"); i >= 0 {
		task = task[i+1:]
	}
	output := r.Output
	cut := len(output) > MaxRetryOutput
	if cut {
		output = output[len(output)-MaxRetryOutput:]
		// Start on a rune boundary.
		for len(output) > 0 && !utf8.RuneStart(output[0]) {
			output = output[1:]
		}
	}
	var problems []string
	for line := range strings.SplitSeq(strings.TrimSpace(r.Problems), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			problems = append(problems, line)
		}
	}
	text := strings.ToValidUTF8(string(output), "\uFFFD")
	var b strings.Builder
	err := retry.Execute(&b, struct {
		Task, Output, Fence string
		Problems            []string
		Cut                 bool
		Shown               int
	}{
		Task:     strings.TrimSpace(task),
		Output:   strings.TrimRight(text, "\n"),
		Fence:    fence(text),
		Problems: problems,
		Cut:      cut,
		Shown:    len(output),
	})
	if err != nil {
		return "", fmt.Errorf("render retry prompt: %w", err)
	}
	return b.String(), nil
}

// fence returns a Markdown code fence longer than any backtick run in s.
func fence(s string) string {
	longest, run := 0, 0
	for _, c := range s {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
