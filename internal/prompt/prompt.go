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

	"github.com/kvitrvn/raun/internal/knowledge"
)

//go:embed templates
var templates embed.FS

var (
	task = template.Must(template.ParseFS(templates, "templates/task.md.tmpl"))
	lead = template.Must(template.ParseFS(templates, "templates/lead.md.tmpl"))
)

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
