package prompt

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/kvitrvn/raun/internal/agent"
	"github.com/kvitrvn/raun/internal/knowledge"
)

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)\n```")

func TestAnalystRender(t *testing.T) {
	instructions, err := Builtin("analyst")
	if err != nil {
		t.Fatal(err)
	}
	a := Analyst{
		Instructions: instructions,
		Commit:       "3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b",
		Language:     "fr",
		Types:        []knowledge.Type{knowledge.TypePersona, knowledge.TypeRequirement},
		ContextFiles: []string{".raun/context.md"},
		Exclude:      []string{"vendor/**"},
		MaxLines:     40,
	}
	got, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"You are an analyst",
		"Commit analyzed: 3f1c2a9b8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b",
		"in this language: fr",
		"  - .raun/context.md",
		"  - vendor/**",
		"### persona",
		"### requirement",
		"at most 40 lines",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	assertExampleIsValid(t, got, a.Types)
}

func TestAnalystRenderSingleType(t *testing.T) {
	a := Analyst{Instructions: "Custom job.", Commit: "c", Language: "en", Types: []knowledge.Type{knowledge.TypeRequirement}, MaxLines: 10}
	got, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "### persona") || strings.Contains(got, "project context") || !strings.Contains(got, "Custom job.") {
		t.Errorf("unexpected prompt:\n%s", got)
	}
	assertExampleIsValid(t, got, a.Types)
}

// assertExampleIsValid checks that the JSON example shown to agents is
// accepted by the report validator.
func assertExampleIsValid(t *testing.T, prompt string, types []knowledge.Type) {
	t.Helper()
	m := jsonBlock.FindStringSubmatch(prompt)
	if m == nil {
		t.Fatal("no JSON example in prompt")
	}
	if !json.Valid([]byte(m[1])) {
		t.Fatalf("JSON example is not valid JSON:\n%s", m[1])
	}
	if _, _, err := agent.ParseReport([]byte(m[1]), types); err != nil {
		t.Errorf("JSON example rejected by the contract: %v\n%s", err, m[1])
	}
}

func TestBuiltinUnknown(t *testing.T) {
	if _, err := Builtin("poet"); err == nil {
		t.Error("Builtin(unknown) succeeded")
	}
}

func TestLeadRender(t *testing.T) {
	instructions, err := Builtin("lead")
	if err != nil {
		t.Fatal(err)
	}
	for _, types := range [][]knowledge.Type{
		{knowledge.TypePersona, knowledge.TypeRequirement},
		{knowledge.TypeRequirement},
	} {
		l := Lead{Instructions: instructions, Commit: "c0ffee", Language: "fr", Types: types, Reports: `{"reports": []}`}
		got, err := l.Render()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"You are the lead", "Commit analyzed: c0ffee", "this language: fr", `{"reports": []}`, "exactly one item"} {
			if !strings.Contains(got, want) {
				t.Errorf("lead prompt lacks %q", want)
			}
		}

		// The example must satisfy the contract, given matching reports.
		blocks := jsonBlock.FindAllStringSubmatch(got, -1)
		example := blocks[len(blocks)-1][1]
		shown := map[string]knowledge.Type{"A.i1": types[0], "B.i2": types[0]}
		if _, _, err := agent.ParseConsolidation([]byte(example), types, shown); err != nil {
			t.Errorf("lead example rejected by the contract: %v\n%s", err, example)
		}
	}
}
